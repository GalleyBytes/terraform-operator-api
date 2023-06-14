package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"

	"github.com/galleybytes/terraform-operator-api/pkg/common/models"
	tfv1beta1 "github.com/galleybytes/terraform-operator/pkg/apis/tf/v1beta1"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	serializer "k8s.io/apimachinery/pkg/runtime/serializer/json"
	"k8s.io/client-go/kubernetes/scheme"
)

type resource struct {
	tfv1beta1.Terraform `json:",inline"`
	// TFOResourceSpec models.TFOResourceSpec `json:"tfo_resource_spec"`
	// TFOResource     models.TFOResource     `json:"tfo_resource"`
}

func (r resource) validate() error {

	if r.ObjectMeta.UID == "" {
		return errors.New("resource is missing the uuid")
	}
	if r.ObjectMeta.Name == "" {
		return errors.New("resource is missing the name")
	}
	// validate all data is received for the resource
	if r.ObjectMeta.Namespace == "" {
		return errors.New("resource is missing the namespace")
	}
	if r.ObjectMeta.Generation == 0 {
		return errors.New("resource is missing the generation")
	}

	return nil
}

func (r resource) Parse(clusterID uint) (*models.TFOResource, *models.TFOResourceSpec, error) {
	// var tfoResource models.TFOResource
	// var tfoResourceSpec models.TFOResourceSpec

	uuid := string(r.ObjectMeta.UID)
	annotations := mustJsonify(r.Annotations)
	labels := mustJsonify(r.Labels)
	currentGeneration := strconv.FormatInt(r.Generation, 10)
	tfoResource := models.TFOResource{
		Name:              r.Name,
		Namespace:         r.Namespace,
		UUID:              uuid,
		Annotations:       annotations,
		Labels:            labels,
		CurrentGeneration: currentGeneration,
		ClusterID:         clusterID,
	}

	spec, err := jsonify(r.Spec)
	if err != nil {
		return nil, nil, err
	}
	tfoResourceSpec := models.TFOResourceSpec{
		TFOResourceUUID: uuid,
		Generation:      currentGeneration,
		ResourceSpec:    spec,
	}

	return &tfoResource, &tfoResourceSpec, nil
}

func (h APIHandler) AddCluster(c *gin.Context) {

	// TODO cluster name must also have a tenant id which will need to come from the JWT token assigned to the request
	// For this, I still need to implemet a registration service.
	tenantId := "internal"
	_ = tenantId

	jsonData := struct {
		ClusterName string `json:"cluster_name"`
	}{}
	err := c.BindJSON(&jsonData)
	if err != nil {
		log.Println(err)
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, err.Error(), nil))
		return
	}
	if jsonData.ClusterName == "" {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, "missing request data", nil))
		return
	}

	cluster := models.Cluster{
		Name: jsonData.ClusterName,
	}
	result := h.DB.Where(cluster).FirstOrCreate(&cluster)
	if result.Error != nil {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, result.Error.Error(), nil))
		return
	}

	// Check existance of vcluster in namespace
	namespaceName := tenantId + "-" + cluster.Name
	err = h.createNamespace(c, namespaceName)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, err.Error(), nil))
		return
	}

	err = h.createVirtualCluster(c, namespaceName)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, err.Error(), nil))
		return
	}

	c.JSON(http.StatusOK, response(http.StatusOK, "", []models.Cluster{cluster}))
}

func (h APIHandler) createNamespace(c *gin.Context, namespaceName string) error {
	_, err := h.clientset.CoreV1().Namespaces().Get(c, namespaceName, metav1.GetOptions{})
	if err != nil {
		if kerrors.IsNotFound(err) {
			_, err = h.clientset.CoreV1().Namespaces().Create(c, &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        namespaceName,
					Labels:      map[string]string{},
					Annotations: map[string]string{},
				},
				Spec: corev1.NamespaceSpec{},
			}, metav1.CreateOptions{})
			if err != nil {
				if !kerrors.IsAlreadyExists(err) {
					return err
				}
			}
		} else {
			return err
		}
	}
	return nil
}

func (h APIHandler) createVirtualCluster(c *gin.Context, namespace string) error {
	// Add vcluster install

	// Should this call block until the vcluster is up and running?

	return nil
}

func (h APIHandler) getClusterID(clusterName string) uint {
	// TODO Use a temporary cache to store clusterName to remove a db lookup
	var clusters []models.Cluster
	if result := h.DB.Where("name = ?", clusterName).First(&clusters); result.Error != nil {
		return 0
	}
	return clusters[0].ID
}

// ResourcePoll is a short poll that checks and returns resources created from the tf resource's workflow
//
// TODO Currently only secrets are expected to be created by the workflow. Resources created outside of the
//
//	normal workflow are ignored. Is it possible to extend get a more extensive list of resources to return?
//	One issue with this is that TFO does not have a method to track those resources.
//
// TODO #2 require a special return labels for resources to return back to originating cluster
func (h APIHandler) ResourcePoll(c *gin.Context) {
	resourceUUID := c.Param("tfo_resource_uuid")

	// Before checking for resources to return, check that the current generation has completed
	tf, err := h.tfoclientset.TfV1beta1().Terraforms("default").Get(c, resourceUUID, metav1.GetOptions{})
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, err.Error(), nil))
		return
	}
	if tf.Status.Stage.Generation != tf.Generation || tf.Status.Stage.Reason != "COMPLETED_APPLY" {
		c.JSON(http.StatusOK, response(http.StatusOK, fmt.Sprintf("The '%s' workflow has not completed.", tf.Name), nil))
		return
	}
	if h.Queue.Len() > 0 {
		// Wait for all queue to complete before opening up for polling
		c.JSON(http.StatusOK, response(http.StatusOK, "Waiting for API to complete queue.", nil))
		return
	}

	// Check if this contains known resources to return
	outputsSecretName := ""
	if tf.Spec.WriteOutputsToStatus {
		// https://github.com/galleybytes/terraform-operator/blob/master/pkg/controllers/terraform_controller.go#L278
		outputsSecretName = fmt.Sprintf("%-v%s", tf.Status.PodNamePrefix, fmt.Sprint(tf.Generation))
	}
	if tf.Spec.OutputsSecret != "" {
		outputsSecretName = tf.Spec.OutputsSecret
	}
	if outputsSecretName == "" {
		c.JSON(http.StatusOK, response(http.StatusOK, fmt.Sprintf("The '%s' workflow does not create secrets", tf.Name), nil))
	}

	secretClient := h.clientset.CoreV1().Secrets("default")
	secret, err := secretClient.Get(c, outputsSecretName, metav1.GetOptions{})
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, err.Error(), nil))
		return
	}
	if originalOutputsSecretName, found := tf.Annotations["tfo.galleybytes.com/outputsSecret"]; found {
		// When this annotation is found in the tf-resource, rename the secret before returning it
		secret.Name = originalOutputsSecretName
	}
	gvks, _, err := scheme.Scheme.ObjectKinds(secret)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, err.Error(), nil))
		return
	}
	if len(gvks) == 0 {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, "Resource is does not have a registered version", nil))
		return
	}
	secret.SetGroupVersionKind(gvks[0])

	buf := bytes.NewBuffer([]byte{})
	serializer.
		NewSerializer(serializer.DefaultMetaFactory, runtime.NewScheme(), runtime.NewScheme(), true).
		Encode(secret, buf)

	resources := []string{}
	resources = append(resources, buf.String())

	c.JSON(http.StatusOK, response(http.StatusOK, "", resources))
}

func (h APIHandler) ResourceEvent(c *gin.Context) {
	if c.Request.Method == http.MethodPost {
		err := h.addResource(c)
		if err != nil {
			log.Println(err)
			c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, err.Error(), nil))
			return
		}
		c.JSON(http.StatusNoContent, nil)
		return
	}

	if c.Request.Method == http.MethodPut {
		err := h.updateResource(c)
		if err != nil {
			log.Println(err)
			c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, err.Error(), nil))
			return
		}
		c.JSON(http.StatusNoContent, nil)
		return
	}

	if c.Request.Method == http.MethodDelete {
		c.JSON(http.StatusOK, response(http.StatusOK, "", []string{"No handler created for DELETE event yet"}))
		return
	}

	c.JSON(http.StatusNotFound, response(http.StatusNotFound, "", []string{}))
}

func (h APIHandler) addResource(c *gin.Context) error {
	clusterName := c.Param("cluster_name")
	clusterID := h.getClusterID(clusterName)
	if clusterID == 0 {
		return fmt.Errorf("cluster_name '%s' not found", clusterName)
	}

	jsonData := resource{}
	err := c.BindJSON(&jsonData)
	if err != nil {
		log.Println("Unable to parse JSON from request data")
		return errors.New("Unable to parse JSON from request data: " + err.Error())
	}

	// if tfoResource.UUID == "" {}
	err = jsonData.validate()
	if err != nil {
		return err
	}

	tfoResource, tfoResourceSpec, err := jsonData.Parse(clusterID)
	if err != nil {
		return err
	}

	cluster := &models.Cluster{
		Model: gorm.Model{
			ID: clusterID,
		},
	}
	result := h.DB.First(cluster)
	if result.Error != nil {
		// cluster must exist prior to adding resources
		return result.Error
	}

	// new resources must not already exist in the database
	// var x models.TFOResource
	result = h.DB.First(&tfoResource)
	if result.Error == nil {
		// the UUID exists in the database
		return errors.New("TFOResource already exists in database")
	} else if result.Error != nil && !errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return fmt.Errorf("error occurred when looking for tfo_resource: %v", result.Error)
	}

	result = h.DB.Create(&tfoResource)
	if result.Error != nil {
		return fmt.Errorf("error saving tfo_resource: %s", result.Error)
	}

	result = h.DB.Create(&tfoResourceSpec)
	if result.Error != nil {
		return fmt.Errorf("error saving tfo_resource_spec: %s", result.Error)
	}

	h.appendClusterNameLabel(&jsonData.Terraform, cluster.Name)
	// TODO Allow using a different queue
	h.Queue.PushBack(jsonData.Terraform)

	return nil
}

func (h APIHandler) updateResource(c *gin.Context) error {
	clusterName := c.Param("cluster_name")
	clusterID := h.getClusterID(clusterName)
	if clusterID == 0 {
		return fmt.Errorf("cluster_name '%s' not found", clusterName)
	}

	jsonData := resource{}
	err := c.BindJSON(&jsonData)
	if err != nil {
		log.Println("Unable to parse JSON from request data")
		return errors.New("Unable to parse JSON from request data: " + err.Error())
	}

	// if tfoResource.UUID == "" {}
	err = jsonData.validate()
	if err != nil {
		return err
	}

	tfoResource, tfoResourceSpec, err := jsonData.Parse(clusterID)
	if err != nil {
		return err
	}

	cluster := &models.Cluster{
		Model: gorm.Model{
			ID: clusterID,
		},
	}
	result := h.DB.First(cluster)
	if result.Error != nil {
		// cluster must exist prior to adding resources
		return fmt.Errorf("error getting cluster: %v", result.Error)
	}

	// new resources must not already exist in the database
	tfoResourceFromDatabase := models.TFOResource{}
	result = h.DB.Where("uuid = ?", tfoResource.UUID).First(&tfoResourceFromDatabase)
	if result.Error != nil {
		// result must exist to update
		return fmt.Errorf("error getting tfoResource: %v", result.Error)
	}

	gen1 := tfoResource.CurrentGeneration
	gen2 := tfoResourceFromDatabase.CurrentGeneration
	if compare(gen1, "<", gen2) {
		return fmt.Errorf("error updating resource, generation '%s' is less than current generation '%s'", gen1, gen2)
	}

	result = h.DB.Save(&tfoResource)
	if result.Error != nil {
		return result.Error
	}

	tfoResourceSpecFromDatabase := models.TFOResourceSpec{}
	result = h.DB.Where("tfo_resource_uuid = ? AND generation = ?", tfoResource.UUID, tfoResource.CurrentGeneration).First(&tfoResourceSpecFromDatabase)
	if result.Error != nil && errors.Is(result.Error, gorm.ErrRecordNotFound) {
		result = h.DB.Create(&tfoResourceSpec)
		if result.Error != nil {
			return result.Error
		}
	} else if result.Error != nil {
		return fmt.Errorf("error occurred when looking for tfo_resource_spec: %v", result.Error)
	}

	h.appendClusterNameLabel(&jsonData.Terraform, cluster.Name)
	// TODO Allow using a different queue
	h.Queue.PushBack(jsonData.Terraform)

	return nil
}

// appendClusterNameLabel will hack the cluster name to the resource's labels.
// This make it easier to identify the origin of the resource in a remote cluster.
func (h APIHandler) appendClusterNameLabel(tf *tfv1beta1.Terraform, clusterName string) {
	if clusterName == "" {
		return
	}
	if tf.Labels == nil {
		tf.Labels = map[string]string{}
	}
	tf.Labels["tfo-api.galleybytes.com/cluster-name"] = clusterName
}

func compare(s1, op, s2 string) bool {
	i1, err := strconv.Atoi(s1)
	if err != nil {
		log.Panic(err)
	}

	i2, err := strconv.Atoi(s2)
	if err != nil {
		log.Panic(err)
	}

	switch op {
	case ">":
		return i1 > i2
	case ">=":
		return i1 >= i2
	case "==":
		return i1 == i2
	case "<":
		return i1 < i2
	case "<=":
		return i1 <= i2
	default:
		return i1 == i2
	}
}

func mustJsonify(o interface{}) string {
	b, err := json.Marshal(o)
	if err != nil {
		log.Printf("ERROR marshaling data: %+v", o)
	}
	if len(b) == 0 {
		// make a valid empty json object
		b = []byte("{}")
	}
	return string(b)
}

func jsonify(o interface{}) (string, error) {
	b, err := json.Marshal(o)
	if err != nil {
		return "", err
	}
	if len(b) == 0 {
		// make a valid empty json object
		b = []byte("{}")
	}
	return string(b), nil
}
