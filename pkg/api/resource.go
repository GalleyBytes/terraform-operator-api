package api

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/galleybytes/terraform-operator-api/pkg/common/models"
	"github.com/galleybytes/terraform-operator-api/pkg/util"
	tfv1beta1 "github.com/galleybytes/terraform-operator/pkg/apis/tf/v1beta1"
	tfo "github.com/galleybytes/terraform-operator/pkg/client/clientset/versioned"
	"github.com/gin-gonic/gin"
	"github.com/isaaguilar/kedge"
	"gopkg.in/yaml.v3"
	"gorm.io/gorm"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8sjson "k8s.io/apimachinery/pkg/runtime/serializer/json"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
)

//go:embed manifests/vcluster.tpl.yaml
var defaultVirtualClusterManifestTemplate string

type rawData map[string][]byte

type resource struct {
	tfv1beta1.Terraform `json:",inline"`
	// TFOResourceSpec models.TFOResourceSpec `json:"tfo_resource_spec"`
	// TFOResource     models.TFOResource     `json:"tfo_resource"`
}

type PodInfo struct {
	Name      string
	CreatedAt time.Time
}

// ByCreatedAt implements sort.Interface for []PodInfo based on the CreatedAt field
type ByCreatedAt []PodInfo

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
		ClusterName      string `json:"cluster_name"`
		ClusterManifest  []byte `json:"clusterManifest"`
		VClusterManifest []byte `json:"vClusterManifest`
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

	// TODO expand the manifest file to be a variable to change the creation options of the vcluster
	// Creates the VCluster by applying the static manifest template
	vClusterManifest := []byte(defaultVirtualClusterManifestTemplate)
	if len(jsonData.VClusterManifest) > 0 {
		vClusterManifest = jsonData.VClusterManifest
	}
	err = h.applyRawManifest(c, kedge.KubernetesConfig(os.Getenv("KUBECONFIG")), vClusterManifest, namespaceName)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, fmt.Sprintf("could not create vcluster: %s", err), nil))
		return
	}

	if len(jsonData.ClusterManifest) > 0 {
		err = h.applyRawManifest(c, kedge.KubernetesConfig(os.Getenv("KUBECONFIG")), jsonData.ClusterManifest, namespaceName)
		if err != nil {
			c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, fmt.Sprintf("could not create cluster resources: %s", err), nil))
			return
		}
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

// applyRawManifest will create resources by applying the static manifest template
func (h APIHandler) applyRawManifest(c *gin.Context, config *rest.Config, raw []byte, namespace string) error {
	tempfile, err := os.CreateTemp(util.Tmpdir(), "*manifest")
	if err != nil {
		return err
	}
	defer os.Remove(tempfile.Name())
	// fmt.Println("Created file", tempfile.Name())

	err = os.WriteFile(tempfile.Name(), raw, 0755)
	if err != nil {
		return fmt.Errorf("whoa! error here: %s", err)
	}

	err = kedge.Apply(config, tempfile.Name(), namespace, []string{})
	if err != nil {
		re := regexp.MustCompile(`namespaces.*not found`)
		if re.Match([]byte(err.Error())) {
			client, err := kubernetes.NewForConfig(config)
			if err != nil {
				return err
			}
			_, err = client.CoreV1().Namespaces().Create(c, &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespace,
				},
			}, metav1.CreateOptions{})
			if err != nil {
				if !kerrors.IsAlreadyExists(err) {
					return err
				}
			}
			return h.applyRawManifest(c, config, raw, namespace)
		}
		return fmt.Errorf("error applying manifest: %s", err)
	}
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

func (h APIHandler) VClusterHealth(c *gin.Context) {
	clusterName := c.Param("cluster_name")
	clusterID := h.getClusterID(clusterName)
	if clusterID == 0 {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, fmt.Sprintf("cluster_name '%s' not found", clusterName), nil))
		return
	}
	config, err := getVclusterConfig(h.clientset, "internal", clusterName)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, err.Error(), nil))
		return
	}
	clientset := kubernetes.NewForConfigOrDie(config)
	n, err := clientset.CoreV1().Namespaces().List(c, metav1.ListOptions{})
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, err.Error(), nil))
		return
	}
	if len(n.Items) == 0 {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, "No namespaces available", nil))
		return
	}
	c.JSON(http.StatusNoContent, nil)
}

func (h APIHandler) VClusterTFOHealth(c *gin.Context) {
	clusterName := c.Param("cluster_name")
	clusterID := h.getClusterID(clusterName)
	if clusterID == 0 {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, fmt.Sprintf("cluster_name '%s' not found", clusterName), nil))
		return
	}
	config, err := getVclusterConfig(h.clientset, "internal", clusterName)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, err.Error(), nil))
		return
	}
	tfoclientset := tfo.NewForConfigOrDie(config)
	if _, err = tfoclientset.TfV1beta1().Terraforms("").List(c, metav1.ListOptions{}); err != nil {
		// tfo client cannot query crds and therefore tfo health is not ready
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, err.Error(), nil))
		return
	}

	// Following checks if tfo is running. TFO must be installed similar to the bundled packages in the
	// terraform-operator github repo.
	clientset := kubernetes.NewForConfigOrDie(config)
	n, err := clientset.CoreV1().Pods("tf-system").List(c, metav1.ListOptions{
		LabelSelector: "app=terraform-operator,component=controller",
		FieldSelector: "status.phase=Running",
	})
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, err.Error(), nil))
		return
	}
	if len(n.Items) == 0 {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, "Terraform operator controller is not running", nil))
		return
	}
	c.JSON(http.StatusNoContent, nil)
}

func (h APIHandler) ResourceStatusCheck(c *gin.Context) {
	clusterName := c.Param("cluster_name")
	clusterID := h.getClusterID(clusterName)
	if clusterID == 0 {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, fmt.Sprintf("cluster_name '%s' not found", clusterName), nil))
		return
	}

	name := c.Param("name")
	namespace := c.Param("namespace")

	resource, err := getResource(h.clientset, clusterName, namespace, name, c)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, fmt.Sprintf("tf resource '%s/%s' not found", namespace, name), nil))
		return
	}

	responseJSONData := []struct {
		DidStart     bool   `json:"did_start"`
		DidComplete  bool   `json:"did_complete"`
		CurrentState string `json:"current_state"`
		CurrentTask  string `json:"current_task"`
	}{
		{
			DidStart:     resource.Generation == resource.Status.Stage.Generation,
			DidComplete:  !IsWorkflowRunning(resource.Status),
			CurrentState: string(resource.Status.Stage.State),
			CurrentTask:  resource.Status.Stage.TaskType.String(),
		},
	}

	c.JSON(http.StatusOK, response(http.StatusOK, "", responseJSONData))

}

func (a ByCreatedAt) Len() int           { return len(a) }
func (a ByCreatedAt) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a ByCreatedAt) Less(i, j int) bool { return a[i].CreatedAt.Before(a[j].CreatedAt) }

func (h APIHandler) LastTaskLog(c *gin.Context) {
	clusterName := c.Param("cluster_name")
	clusterID := h.getClusterID(clusterName)

	if clusterID == 0 {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, fmt.Sprintf("cluster_name '%s' not found", clusterName), nil))
		return
	}

	resourceName := c.Param("name")
	namespace := c.Param("namespace")

	config, err := getVclusterConfig(h.clientset, "internal", clusterName)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, err.Error(), nil))
		return
	}

	clientset := kubernetes.NewForConfigOrDie(config)

	// check if namespace exists before querying for pods by label
	_, err = clientset.CoreV1().Namespaces().Get(context.Background(), namespace, metav1.GetOptions{})
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, err.Error(), nil))
		return
	}

	// get the pods with a certain label in the default namespace
	labelSelector := "terraforms.tf.galleybytes.com/resourceName=" + resourceName // change this to your label selector
	pods, err := clientset.CoreV1().Pods(namespace).List(context.Background(), metav1.ListOptions{LabelSelector: labelSelector})
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, err.Error(), nil))
		return
	}

	// check if pods were found with matching labels
	if len(pods.Items) == 0 {
		c.JSON(http.StatusNotFound, response(http.StatusNotFound, fmt.Sprintf("terraform pods not found on cluster '%s' for tf resource '%s/%s'", clusterName, namespace, resourceName), nil))
		return
	}

	// create a slice of PodInfo from the pods
	podInfos := make([]PodInfo, 0, len(pods.Items))

	for _, pod := range pods.Items {
		podInfos = append(podInfos, PodInfo{Name: pod.Name, CreatedAt: pod.CreationTimestamp.Time})
	}

	// sort the podInfos by creation timestamp in ascending order
	sort.Sort(ByCreatedAt(podInfos))

	// get the name of the newest pod
	newestPod := podInfos[len(podInfos)-1].Name

	pod, err := clientset.CoreV1().Pods(namespace).Get(context.Background(), newestPod, metav1.GetOptions{})
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, err.Error(), nil))
		return
	}

	currentTask := ""
	// find the environment variable
	for _, container := range pod.Spec.Containers {
		for _, envVar := range container.Env {
			if envVar.Name == "TFO_TASK" {
				currentTask = envVar.Value
			}
		}
	}

	// get the logs of the newest pod
	logs, err := clientset.CoreV1().Pods(namespace).GetLogs(newestPod, &corev1.PodLogOptions{}).DoRaw(context.Background())
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, err.Error(), nil))
		return
	}

	ansiColorRegex := regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)
	cleanString := ansiColorRegex.ReplaceAllString(string(logs), "")

	responseJSONData := []struct {
		ClusterName string `json:"cluster_name"`
		Namespace   string `json:"namespace"`
		TFOResource string `json:"tfo_resource"`
		CurrentTask string `json:"current_task"`
		LastTaskLog string `json:"last_task_log"`
	}{
		{
			ClusterName: string(clusterName),
			Namespace:   string(namespace),
			TFOResource: string(resourceName),
			CurrentTask: string(currentTask),
			LastTaskLog: string(cleanString),
		},
	}

	c.JSON(http.StatusOK, response(http.StatusOK, "", responseJSONData))

}

// Take into account pod phases as well as task state to determine if the workflow is still running.
func IsWorkflowRunning(status tfv1beta1.TerraformStatus) bool {
	switch status.Stage.State {
	case tfv1beta1.StateFailed, tfv1beta1.StageState(corev1.PodFailed):
		// Failed states trump all, the workflow is not running after a failure
		return false
	case tfv1beta1.StageState(corev1.PodRunning), tfv1beta1.StageState(corev1.PodPending):
		// When the pod is claimed to be running, the workflow is still running
		return true
	default:
		if status.Phase != tfv1beta1.PhaseCompleted {
			return true
		}
		return false
	}
}

// ResourcePoll is a short poll that checks and returns resources created from the tf resource's workflow
// that have the correct label and annotation value.
func (h APIHandler) ResourcePoll(c *gin.Context) {
	clusterName := c.Param("cluster_name")
	clusterID := h.getClusterID(clusterName)
	if clusterID == 0 {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, fmt.Sprintf("cluster_name '%s' not found", clusterName), nil))
		return
	}

	name := c.Param("name")
	namespace := c.Param("namespace")

	config, err := getVclusterConfig(h.clientset, "internal", clusterName)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, err.Error(), nil))
		return
	}
	tfoclientset := tfo.NewForConfigOrDie(config)
	clientset := kubernetes.NewForConfigOrDie(config)

	// Before checking for resources to return, check that the current generation has completed
	tf, err := tfoclientset.TfV1beta1().Terraforms(namespace).Get(c, name, metav1.GetOptions{})
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, err.Error(), nil))
		return
	}
	if tf.Status.Stage.Generation != tf.Generation || !regexp.MustCompile(`COMPLETED.*APPLY`).MatchString(tf.Status.Stage.Reason) {
		c.JSON(http.StatusOK, response(http.StatusOK, fmt.Sprintf("The '%s' workflow has not completed.", tf.Name), nil))
		return
	}

	resourceList := corev1.List{}
	resourceList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "",
		Version: "v1",
		Kind:    "List",
	})

	annotationKey := "tfo-api.galleybytes.com/sync-upon-completion-of"
	labelSelector := "tfo-api.galleybytes.com/sync"
	secrets, err := clientset.CoreV1().Secrets(namespace).List(c, metav1.ListOptions{
		LabelSelector: labelSelector, // Find resources when the key exists
	})
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, err.Error(), nil))
		return
	}
	for _, secret := range secrets.Items {
		syncUponCompletionOf := []string{}
		err := yaml.Unmarshal([]byte(secret.Annotations[annotationKey]), &syncUponCompletionOf)
		if err != nil {
			log.Printf("ERROR: sync labels is improperly formatted for %s/%s/%s", clusterName, namespace, secret.Name)
			continue
		}
		if !util.Contains(syncUponCompletionOf, name) {
			continue
		}

		gvks, _, err := scheme.Scheme.ObjectKinds(&secret)
		if err != nil {
			c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, err.Error(), nil))
			return
		}
		if len(gvks) == 0 {
			c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, "Could not decode secret GroupVersionKind", nil))
			return
		}
		secret.SetGroupVersionKind(gvks[0])

		buf := bytes.NewBuffer([]byte{})
		k8sjson.NewSerializer(k8sjson.DefaultMetaFactory, runtime.NewScheme(), runtime.NewScheme(), true).Encode(&secret, buf)

		resourceList.Items = append(resourceList.Items, runtime.RawExtension{
			Raw:    buf.Bytes(),
			Object: &secret,
		})
	}

	configMaps, err := clientset.CoreV1().ConfigMaps(namespace).List(c, metav1.ListOptions{})
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, err.Error(), nil))
		return
	}
	for _, configMap := range configMaps.Items {
		syncUponCompletionOf := []string{}
		err := yaml.Unmarshal([]byte(configMap.Annotations[annotationKey]), &syncUponCompletionOf)
		if err != nil {
			log.Printf("ERROR: sync labels is improperly formatted for %s/%s/%s", clusterName, namespace, configMap.Name)
			continue
		}
		if !util.Contains(syncUponCompletionOf, name) {
			continue
		}

		gvks, _, err := scheme.Scheme.ObjectKinds(&configMap)
		if err != nil {
			c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, err.Error(), nil))
			return
		}
		if len(gvks) == 0 {
			c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, "Could not decode configMap GroupVersionKind", nil))
			return
		}
		configMap.SetGroupVersionKind(gvks[0])

		buf := bytes.NewBuffer([]byte{})
		k8sjson.NewSerializer(k8sjson.DefaultMetaFactory, runtime.NewScheme(), runtime.NewScheme(), true).Encode(&configMap, buf)

		// tf.Spec.
		resourceList.Items = append(resourceList.Items, runtime.RawExtension{
			Raw:    buf.Bytes(),
			Object: &configMap,
		})
	}

	resources := [][]byte{}
	resources = append(resources, raw(resourceList))
	c.JSON(http.StatusOK, response(http.StatusOK, "", resources))
}

func raw(o interface{}) []byte {
	b, err := json.Marshal(o)
	if err != nil {
		log.Panic(err)
	}
	var out bytes.Buffer
	err = json.Indent(&out, b, "", "  ")
	if err != nil {
		log.Panic(err)
	}
	return out.Bytes()
}

func (h APIHandler) SyncEvent(c *gin.Context) {
	if c.Request.Method == http.MethodPut {
		err := h.syncDependencies(c)
		if err != nil {
			log.Println(err)
			c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, err.Error(), nil))
			return
		}
		c.JSON(http.StatusNoContent, nil)
		return
	}
}

func (h APIHandler) syncDependencies(c *gin.Context) error {
	clusterName := c.Param("cluster_name")
	clusterID := h.getClusterID(clusterName)
	if clusterID == 0 {
		return fmt.Errorf("cluster_name '%s' not found", clusterName)
	}

	jsonData := rawData{}
	err := c.BindJSON(&jsonData)
	if err != nil {
		log.Println("Unable to parse JSON from request data")
		return errors.New("Unable to parse JSON from request data: " + err.Error())
	}

	raw := jsonData["raw"]
	namespace := jsonData["namespace"]
	if raw != nil && namespace != nil {

		config, err := getVclusterConfig(h.clientset, "internal", clusterName)
		if err != nil {
			return err
		}
		return h.applyRawManifest(c, config, raw, string(namespace))
	}
	return nil
}

func getVclusterConfig(clientset kubernetes.Interface, tenantId, clusterName string) (*rest.Config, error) {
	// With the clusterName, check out the vcluster config
	namespace := tenantId + "-" + clusterName
	secret, err := clientset.CoreV1().Secrets(namespace).Get(context.TODO(), "vc-tfo-virtual-cluster", metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	kubeConfigData := secret.Data["config"]
	if len(kubeConfigData) == 0 {
		return nil, errors.New("no config data found for vcluster kubeconfig")
	}

	kubeConfigFilename, err := os.CreateTemp(util.Tmpdir(), "kubeconfig-*")
	if err != nil {
		return nil, fmt.Errorf("could nto create tempfile to write vcluster kubeconfig: %s", err)
	}
	defer os.Remove(kubeConfigFilename.Name())
	err = os.WriteFile(kubeConfigFilename.Name(), kubeConfigData, 0755)
	if err != nil {
		return nil, fmt.Errorf("the vcluster kubeconfig file could not be saved: %s", err)
	}

	// We have to make an insecure request to the cluster because the vcluster has a cert thats valid for localhost.
	// To do so we set the config to insecure and we remove the CAData. We have to leave
	// CertData and CertFile which are used as authorization to the vcluster.
	config := kedge.KubernetesConfig(kubeConfigFilename.Name())
	config.Host = fmt.Sprintf("tfo-virtual-cluster.%s.svc", namespace)
	config.Insecure = true
	config.TLSClientConfig.CAData = nil
	return config, nil
}

func (h APIHandler) ResourceEvent(c *gin.Context) {
	if c.Request.Method == http.MethodPost {
		msg, err := h.addResource(c)
		if err != nil {
			log.Println(err)
			c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, err.Error(), nil))
			return
		} else if msg != "" {
			c.JSON(http.StatusOK, response(http.StatusOK, msg, []string{}))
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

func (h APIHandler) addResource(c *gin.Context) (string, error) {
	clusterName := c.Param("cluster_name")
	clusterID := h.getClusterID(clusterName)
	if clusterID == 0 {
		return "", fmt.Errorf("cluster_name '%s' not found", clusterName)
	}

	jsonData := resource{}
	err := c.BindJSON(&jsonData)
	if err != nil {
		log.Println("Unable to parse JSON from request data")
		return "", errors.New("Unable to parse JSON from request data: " + err.Error())
	}

	// if tfoResource.UUID == "" {}
	err = jsonData.validate()
	if err != nil {
		return "", err
	}

	tfoResource, tfoResourceSpec, err := jsonData.Parse(clusterID)
	if err != nil {
		return "", err
	}

	cluster := &models.Cluster{
		Model: gorm.Model{
			ID: clusterID,
		},
	}
	result := h.DB.First(cluster)
	if result.Error != nil {
		// cluster must exist prior to adding resources
		return "", result.Error
	}

	// new resources must not already exist in the database
	// var x models.TFOResource
	result = h.DB.First(&tfoResource)
	if result.Error == nil {
		// the UUID exists in the database. The result will return a success with the message the resource
		// already exists. To update the resource_spec, the client must make a PUT request instead.
		return fmt.Sprintf("tf resource '%s/%s' already exists", tfoResource.Namespace, tfoResource.Name), nil
	} else if result.Error != nil && !errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return "", fmt.Errorf("error occurred when looking for tfo_resource: %v", result.Error)
	}

	result = h.DB.Create(&tfoResource)
	if result.Error != nil {
		return "", fmt.Errorf("error saving tfo_resource: %s", result.Error)
	}

	result = h.DB.Create(&tfoResourceSpec)
	if result.Error != nil {
		return "", fmt.Errorf("error saving tfo_resource_spec: %s", result.Error)
	}

	apiURL := getApiURL(c, h.serviceIP)
	token := fetchToken(h.DB, *tfoResourceSpec, h.tenant, clusterName, apiURL)
	appendClusterNameLabel(&jsonData.Terraform, cluster.Name)
	addOriginEnvs(&jsonData.Terraform, h.tenant, clusterName, apiURL, token)

	err = applyOnCreateOrUpdate(c, jsonData.Terraform, h.clientset, h.tenant)
	if err != nil {
		return "", err
	}

	return "", nil
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
		tfoResourceSpecFromDatabase = *tfoResourceSpec
	} else if result.Error != nil {
		return fmt.Errorf("error occurred when looking for tfo_resource_spec: %v", result.Error)
	}

	apiURL := getApiURL(c, h.serviceIP)
	token := fetchToken(h.DB, tfoResourceSpecFromDatabase, h.tenant, clusterName, apiURL)
	appendClusterNameLabel(&jsonData.Terraform, clusterName)
	addOriginEnvs(&jsonData.Terraform, h.tenant, clusterName, apiURL, token)

	err = applyOnCreateOrUpdate(c, jsonData.Terraform, h.clientset, h.tenant)
	if err != nil {
		return err
	}

	return nil
}

func fetchToken(db *gorm.DB, tfoResourceSpec models.TFOResourceSpec, tenant, clusterName, apiURL string) string {

	generation := tfoResourceSpec.Generation
	resourceUUID := tfoResourceSpec.TFOResourceUUID
	token := tfoResourceSpec.TaskToken

	if token == "" {
		t, err := generateTaskJWT(resourceUUID, tenant, clusterName, generation)
		if err != nil {
			log.Printf("Failed to generate taskJWT: %s", err)
		} else {
			token = t
			tfoResourceSpec.TaskToken = token
			result := db.Save(&tfoResourceSpec)
			if result.Error != nil {
				log.Printf("Failed to save task_token to tfo_resource_specs: %s", result.Error.Error())
			}
		}
	}

	return token
}

func getApiURL(c *gin.Context, serviceIP *string) string {
	if serviceIP != nil {
		if *serviceIP != "" {
			scheme := "http"
			apiHost := *serviceIP
			is443 := strings.HasSuffix(*serviceIP, ":443")
			if is443 {
				scheme = "https"
			}
			return fmt.Sprintf("%s://%s", scheme, apiHost)
		}
	}

	scheme := "http" // default for the gin `Run` function
	apiHost := c.Request.Host
	is443 := strings.HasSuffix(apiHost, ":443")
	xForwarededHost := c.Request.Header.Get("x-forwarded-host")
	xForwardedScheme := c.Request.Header.Get("x-forwarded-scheme")
	if is443 {
		scheme = "https"
	}
	if xForwarededHost != "" {
		apiHost = xForwarededHost
	}
	if xForwardedScheme != "" {
		scheme = xForwardedScheme
	}

	return fmt.Sprintf("%s://%s", scheme, apiHost)
}

// appendClusterNameLabel will hack the cluster name to the resource's labels.
// This make it easier to identify the origin of the resource in a remote cluster.
func appendClusterNameLabel(tf *tfv1beta1.Terraform, clusterName string) {
	if clusterName == "" {
		return
	}
	if tf.Labels == nil {
		tf.Labels = map[string]string{}
	}
	tf.Labels["tfo-api.galleybytes.com/cluster-name"] = clusterName
}

// addOriginEnvs will inject TFO_ORIGIN envs to the incoming resource
func addOriginEnvs(tf *tfv1beta1.Terraform, tenant, clusterName, apiURL, token string) {
	generation := fmt.Sprintf("%d", tf.Generation)
	resourceUUID := string(tf.UID)
	tf.Spec.TaskOptions = append(tf.Spec.TaskOptions, tfv1beta1.TaskOption{
		For: []tfv1beta1.TaskName{"*"},
		Env: []corev1.EnvVar{
			{
				Name:  "TFO_ORIGIN_UUID",
				Value: resourceUUID,
			},
			{
				Name:  "TFO_ORIGIN_GENERATION",
				Value: generation,
			},
			{
				Name:  "TFO_API_LOG_TOKEN",
				Value: token,
			},
			{
				Name:  "TFO_API_URL",
				Value: apiURL,
			},
		},
	})
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

func applyOnCreateOrUpdate(ctx context.Context, tf tfv1beta1.Terraform, clientset kubernetes.Interface, _tenantID string) error {
	// tenantID is totally broken right now. Just hardcode this for now
	tenantID := "internal"

	labelKey := "tfo-api.galleybytes.com/cluster-name"
	clusterName := tf.Labels[labelKey]
	if clusterName == "" {
		// The terraform resource requires a cluster name in order to continue
		log.Printf("'%s/%s' is missing the '%s' label. Skipping", tf.Namespace, tf.Name, labelKey)
		// continue
		return nil
	}

	config, err := getVclusterConfig(clientset, tenantID, clusterName)
	if err != nil {
		return fmt.Errorf("error occurred getting vcluster config for %s-%s %s/%s: %s", tenantID, clusterName, tf.Namespace, tf.Name, err)
	}
	vclusterClient := kubernetes.NewForConfigOrDie(config)
	vclusterTFOClient := tfo.NewForConfigOrDie(config)

	// Try and create the namespace for the tfResource. Acceptable error is if namespace already exists.
	_, err = vclusterClient.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: tf.Namespace,
		},
	}, metav1.CreateOptions{})
	if err != nil {
		if !kerrors.IsAlreadyExists(err) {
			return fmt.Errorf("namespace/%s could not be created in vcluster: %s", tf.Namespace, err)
		}
	}

	// Cleanup fields that shouldn't exist when creating resources
	tf.SetResourceVersion("")
	tf.SetUID("")
	tf.SetSelfLink("")
	tf.SetGeneration(0)
	tf.SetManagedFields(nil)
	tf.SetCreationTimestamp(metav1.Time{})

	// Get a list of resources in the vcluster to see if the resource already exists to determines whether to patch or create.
	terraforms, err := vclusterTFOClient.TfV1beta1().Terraforms(tf.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("error occurred listing tf objects in vcluster: %s", err)
	}
	isPatch := false // True when terraform resource exists
	for _, terraform := range terraforms.Items {
		if terraform.Name == tf.Name {
			tf.SetManagedFields(terraform.GetManagedFields())
			tf.SetOwnerReferences(terraform.GetOwnerReferences())
			tf.SetDeletionGracePeriodSeconds(terraform.GetDeletionGracePeriodSeconds())
			tf.SetDeletionTimestamp(terraform.GetDeletionTimestamp())
			tf.SetFinalizers(terraform.GetFinalizers())
			tf.SetGenerateName(terraform.GetGenerateName())
			tf.SetResourceVersion(terraform.GetResourceVersion())
			tf.SetUID(terraform.GetUID())
			tf.SetSelfLink(terraform.GetSelfLink())
			tf.SetGeneration(terraform.GetGeneration())
			tf.SetManagedFields(terraform.GetManagedFields())
			tf.SetCreationTimestamp(terraform.GetCreationTimestamp())
			tf.Status = terraform.Status
			isPatch = true
			break
		}
	}

	fswatchImageConfig := tfv1beta1.ImageConfig{
		Image:           "ghcr.io/galleybytes/fswatch:0.10.2",
		ImagePullPolicy: corev1.PullIfNotPresent,
	}
	for _, taskName := range []tfv1beta1.TaskName{
		tfv1beta1.RunSetup,
		tfv1beta1.RunPreInit,
		tfv1beta1.RunInit,
		tfv1beta1.RunPostInit,
		tfv1beta1.RunPrePlan,
		tfv1beta1.RunPlan,
		tfv1beta1.RunPostPlan,
		tfv1beta1.RunPreApply,
		tfv1beta1.RunApply,
		tfv1beta1.RunPostApply,
		tfv1beta1.RunSetupDelete,
		tfv1beta1.RunPreInitDelete,
		tfv1beta1.RunInitDelete,
		tfv1beta1.RunPostInitDelete,
		tfv1beta1.RunPrePlanDelete,
		tfv1beta1.RunPlanDelete,
		tfv1beta1.RunPostPlanDelete,
		tfv1beta1.RunPreApplyDelete,
		tfv1beta1.RunApplyDelete,
		tfv1beta1.RunPostApplyDelete} {
		addSidecar(&tf, taskName+"-fswatch", fswatchImageConfig, taskName, nil)
	}

	addTaskOption(&tf, tfv1beta1.TaskOption{
		For: []tfv1beta1.TaskName{"*"},
		PolicyRules: []rbacv1.PolicyRule{
			{
				Verbs:     []string{"get"},
				Resources: []string{"pods"},
				APIGroups: []string{""},
			},
		},
	})

	if isPatch {
		log.Printf("Patching %s-%s %s/%s", tenantID, clusterName, tf.Namespace, tf.Name)
		err = doPatch(&tf, ctx, tf.Name, tf.Namespace, vclusterTFOClient)
		if err != nil {
			return fmt.Errorf("error occurred patching tf object: %v", err)
			// continue
		}
		log.Printf("Successfully patched %s-%s %s/%s", tenantID, clusterName, tf.Namespace, tf.Name)
		return nil
	}

	err = doCreate(tf, ctx, tf.Namespace, vclusterTFOClient)
	log.Printf("Creating %s-%s %s/%s", tenantID, clusterName, tf.Namespace, tf.Name)
	if err != nil {
		return fmt.Errorf("error creating new tf resource: %v", err)
	}
	log.Printf("Successfully created %s-%s %s/%s", tenantID, clusterName, tf.Namespace, tf.Name)
	return nil
}

func addSidecar(tf *tfv1beta1.Terraform, name tfv1beta1.TaskName, imageConfig tfv1beta1.ImageConfig, task tfv1beta1.TaskName, taskOption *tfv1beta1.TaskOption) {
	if tf.Spec.Plugins == nil {
		tf.Spec.Plugins = make(map[tfv1beta1.TaskName]tfv1beta1.Plugin)
	}
	for key := range tf.Spec.Plugins {
		if key == name {
			return // do not add duplicates
		}
	}

	tf.Spec.Plugins[name] = tfv1beta1.Plugin{
		ImageConfig: imageConfig,
		When:        "Sidecar",
		Task:        task,
		Must:        true,
	}

	if taskOption != nil {
		tf.Spec.TaskOptions = append(tf.Spec.TaskOptions, *taskOption)
	}
}

func addTaskOption(tf *tfv1beta1.Terraform, taskOption tfv1beta1.TaskOption) {
	tf.Spec.TaskOptions = append(tf.Spec.TaskOptions, taskOption)
}

func patchableTFResource(obj *tfv1beta1.Terraform) []byte {
	gvks, _, err := scheme.Scheme.ObjectKinds(obj)
	if err != nil || len(gvks) == 0 {
		obj.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   tfv1beta1.SchemeGroupVersion.Group,
			Version: tfv1beta1.SchemeGroupVersion.Version,
			Kind:    "Terraform",
		})
	}

	buf := bytes.NewBuffer([]byte{})
	k8sjson.NewSerializer(k8sjson.DefaultMetaFactory, runtime.NewScheme(), runtime.NewScheme(), true).Encode(obj, buf)
	return buf.Bytes()
}

func doCreate(new tfv1beta1.Terraform, ctx context.Context, namespace string, client tfo.Interface) error {
	_, err := client.TfV1beta1().Terraforms(namespace).Create(ctx, &new, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("an error occurred saving tf object: %v", err)
	}
	return nil
}

func doPatch(tf *tfv1beta1.Terraform, ctx context.Context, name, namespace string, client tfo.Interface) error {
	_, err := client.TfV1beta1().Terraforms(namespace).Update(ctx, tf, metav1.UpdateOptions{})
	if err != nil {
		return err
	}

	return nil
}
