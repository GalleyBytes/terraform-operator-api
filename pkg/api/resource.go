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

	"github.com/galleybytes/infrakube-stella/pkg/common/models"
	"github.com/galleybytes/infrakube-stella/pkg/util"
	infra3v1 "github.com/galleybytes/infrakube/pkg/apis/infra3/v1"
	infra3clientset "github.com/galleybytes/infrakube/pkg/client/clientset/versioned"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v4"
	"github.com/gorilla/websocket"
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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
)

var VCLUSTER_DEBUG_HOST string = os.Getenv("I3_API_VCLUSTER_DEBUG_HOST")

//go:embed manifests/vcluster.tpl.yaml
var defaultVirtualClusterManifestTemplate string

// Log order:
var taskTypesInOrder = []string{
	"setup",
	"preinit",
	"init",
	"postinit",
	"preplan",
	"plan",
	"postplan",
	"preapply",
	"apply",
	"postapply",
}

type rawData map[string][]byte

type resource struct {
	infra3v1.Tf `json:",inline"`
	// TFOResourceSpec models.TFOResourceSpec `json:"infra3_resource_spec"`
	// TFOResource     models.TFOResource     `json:"infra3_resource"`
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

func (r resource) Parse(clusterID uint) (*models.Infra3Resource, *models.Infra3ResourceSpec, error) {
	// var infra3Resource models.TFOResource
	// var infra3ResourceSpec models.TFOResourceSpec

	uuid := string(r.ObjectMeta.UID)
	annotations := mustJsonify(r.Annotations)
	labels := mustJsonify(r.Labels)
	currentGeneration := strconv.FormatInt(r.Generation, 10)
	infra3Resource := models.Infra3Resource{
		Name:              r.Name,
		Namespace:         r.Namespace,
		UUID:              uuid,
		CurrentGeneration: currentGeneration,
		ClusterID:         clusterID,
		CurrentState:      models.Untracked,
	}

	spec, err := jsonify(r.Spec)
	if err != nil {
		return nil, nil, err
	}
	infra3ResourceSpec := models.Infra3ResourceSpec{
		Infra3ResourceUUID: uuid,
		Generation:         currentGeneration,
		ResourceSpec:       spec,
		Annotations:        annotations,
		Labels:             labels,
	}

	return &infra3Resource, &infra3ResourceSpec, nil
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

func getClusterName(clusterID uint, db *gorm.DB) string {
	var clusters []models.Cluster
	if result := db.Where("id = ?", clusterID).First(&clusters); result.Error != nil {
		return ""
	}
	return clusters[0].Name
}

func getClusterID(clusterName string, db *gorm.DB) uint {
	var clusters []models.Cluster
	if result := db.Where("name = ?", clusterName).First(&clusters); result.Error != nil {
		return 0
	}
	return clusters[0].ID
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

func (h APIHandler) VClusterInfra3Health(c *gin.Context) {
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
	infra3Clientset := infra3clientset.NewForConfigOrDie(config)
	if _, err = infra3Clientset.Infra3V1().Tfs("").List(c, metav1.ListOptions{}); err != nil {
		// infra3 client cannot query crds and therefore infra3 health is not ready
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, err.Error(), nil))
		return
	}

	// Following checks if infra3 is running. TFO must be installed similar to the bundled packages in the
	// infra3 github repo.
	clientset := kubernetes.NewForConfigOrDie(config)
	n, err := clientset.CoreV1().Pods("infra3-system").List(c, metav1.ListOptions{
		LabelSelector: "app=infra3,component=controller",
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

func (h APIHandler) rerunWorkflow(c *gin.Context) {
	clusterName := c.Param("cluster_name")
	clusterID := h.getClusterID(clusterName)
	if clusterID == 0 {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, fmt.Sprintf("cluster_name '%s' not found", clusterName), nil))
		return
	}

	name := c.Param("name")
	namespace := c.Param("namespace")

	err := rerun(h.clientset, clusterName, namespace, name, "api-triggered-rerun", c)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, fmt.Sprintf("Failed to trigger rerun: %s", err), []any{}))
		return
	}
	c.JSON(http.StatusNoContent, nil)
}

func rerun(parentClientset kubernetes.Interface, clusterName, namespace, name, rerunLabelValue string, ctx context.Context) error {
	config, err := getVclusterConfig(parentClientset, "internal", clusterName)
	if err != nil {
		return err
	}
	infra3Clientset := infra3clientset.NewForConfigOrDie(config)
	resource, err := infra3Clientset.Infra3V1().Tfs(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}

	if resource.Labels == nil {
		resource.Labels = map[string]string{}
	}

	resource.Labels["kubernetes.io/change-cause"] = fmt.Sprintf("%s-%s", rerunLabelValue, time.Now().Format("20060102150405"))
	_, err = infra3Clientset.Infra3V1().Tfs(namespace).Update(ctx, resource, metav1.UpdateOptions{})
	if err != nil {
		return err
	}
	return nil
}

type StatusCheckResponse struct {
	DidStart     bool   `json:"did_start"`
	DidComplete  bool   `json:"did_complete"`
	CurrentState string `json:"current_state"`
	CurrentTask  string `json:"current_task"`
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

	statusCheckAndUpdate(c, h.DB, h.clientset, clusterName, namespace, name)
}

func (h APIHandler) ResourceStatusCheckViaTask(c *gin.Context) {
	token, err := taskJWT(c.Request.Header["Token"][0])
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, err.Error(), nil))
		return
	}

	claims := taskJWTClaims(token)
	resourceUUID := claims["resourceUUID"]

	// new resources must not already exist in the database
	infra3ResourceFromDatabase := models.Infra3Resource{}
	result := h.DB.Where("uuid = ?", resourceUUID).First(&infra3ResourceFromDatabase)
	if result.Error != nil {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, fmt.Sprintf("error getting infra3Resource: %v", result.Error), nil))
		return
	}

	clusterName := getClusterName(infra3ResourceFromDatabase.ClusterID, h.DB)
	namespace := infra3ResourceFromDatabase.Namespace
	name := infra3ResourceFromDatabase.Name

	statusCheckAndUpdate(c, h.DB, h.clientset, clusterName, namespace, name)
}

func statusCheckAndUpdate(c *gin.Context, db *gorm.DB, clientset kubernetes.Interface, clusterName, namespace, name string) {
	resource, err := getResource(clientset, clusterName, namespace, name, c)
	if err != nil {
		if kerrors.IsNotFound(err) {
			c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, fmt.Sprintf("tf resource '%s/%s' not found", namespace, name), nil))
			return
		}
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, fmt.Sprintf("tf resource '%s/%s' status check failed: %s", namespace, name, err), nil))
		return
	}

	responseJSONData := []StatusCheckResponse{
		{
			DidStart:     resource.Generation == resource.Status.Stage.Generation,
			DidComplete:  !IsWorkflowRunning(resource.Status),
			CurrentState: string(resource.Status.Stage.State),
			CurrentTask:  resource.Status.Stage.TaskType.String(),
		},
	}

	uuid := ""
OptLoop:
	for _, opt := range resource.Spec.TaskOptions {
		for _, env := range opt.Env {
			if env.Name == "I3_ORIGIN_UUID" {
				uuid = env.Value
				break OptLoop
			}
		}
	}
	if uuid != "" {
		infra3ResourceFromDatabase := models.Infra3Resource{}
		result := db.Where("uuid = ?", uuid).First(&infra3ResourceFromDatabase)
		if result.Error == nil {
			infra3ResourceFromDatabase.CurrentState = models.ResourceState(responseJSONData[0].CurrentState)
			db.Save(infra3ResourceFromDatabase)
		}
	}

	c.JSON(http.StatusOK, response(http.StatusOK, "", responseJSONData))
}

func (h APIHandler) UpdateResourceStatusViaTask(c *gin.Context) {
	jsonData := struct {
		Status string `json:"status"`
	}{}
	err := c.BindJSON(&jsonData)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, fmt.Sprintf("Unable to parse JSON from request data: %s", err), nil))
		return
	}

	token, err := taskJWT(c.Request.Header["Token"][0])
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, err.Error(), nil))
		return
	}

	claims := taskJWTClaims(token)
	resourceUUID := claims["resourceUUID"]

	// new resources must not already exist in the database
	infra3ResourceFromDatabase := models.Infra3Resource{}
	result := h.DB.Where("uuid = ?", resourceUUID).First(&infra3ResourceFromDatabase)
	if result.Error != nil {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, fmt.Sprintf("error getting infra3Resource: %v", result.Error), nil))
		return
	}

	infra3ResourceFromDatabase.CurrentState = models.ResourceState(jsonData.Status)

	result = h.DB.Save(infra3ResourceFromDatabase)
	if result.Error != nil {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, fmt.Sprintf("error updating infra3Resource: %v", result.Error), nil))
		return
	}

	c.JSON(http.StatusNoContent, nil)
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
	labelSelector := "tfs.infra3.galleybytes.com/resourceName=" + resourceName // change this to your label selector
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
			if envVar.Name == "I3_TASK" {
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
		ClusterName    string `json:"cluster_name"`
		Namespace      string `json:"namespace"`
		Infra3Resource string `json:"infra3_resource"`
		CurrentTask    string `json:"current_task"`
		LastTaskLog    string `json:"last_task_log"`
	}{
		{
			ClusterName:    string(clusterName),
			Namespace:      string(namespace),
			Infra3Resource: string(resourceName),
			CurrentTask:    string(currentTask),
			LastTaskLog:    string(cleanString),
		},
	}

	c.JSON(http.StatusOK, response(http.StatusOK, "", responseJSONData))

}

// Take into account pod phases as well as task state to determine if the workflow is still running.
func IsWorkflowRunning(status infra3v1.TfStatus) bool {
	switch status.Stage.State {
	case infra3v1.StateFailed, infra3v1.StageState(corev1.PodFailed):
		// Failed states trump all, the workflow is not running after a failure
		return false
	case infra3v1.StageState(corev1.PodRunning), infra3v1.StageState(corev1.PodPending):
		// When the pod is claimed to be running, the workflow is still running
		return true
	default:
		if status.Phase != infra3v1.PhaseCompleted {
			return true
		}
		return false
	}
}

// Sends logs generally used before opening the websocket. The socket logs will only gather logs
// open cached event items
func (h APIHandler) preLogs(c *gin.Context) {
	infra3ResourceUUID := c.Param("infra3_resource_uuid")
	generation := c.Param("generation")
	rerun := c.Query("rerun")
	_ = rerun
	c.JSON(http.StatusOK, response(http.StatusOK, "", logs(h.DB, infra3ResourceUUID, generation)))
}

func (h APIHandler) websocketLogs(c *gin.Context) {
	infra3ResourceUUID := c.Param("infra3_resource_uuid")
	generation := c.Param("generation")
	rerun := c.Query("rerun")
	_ = rerun

	var wsupgrader = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
	}

	wsupgrader.CheckOrigin = func(r *http.Request) bool { return true }

	conn, err := wsupgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("Failed to set websocket upgrade: %+v", err)
		return
	}
	defer conn.Close()

	s := SocketListener{Connection: conn}
	s.Listen()

	for {
		select {
		case i := <-s.EventType:
			if i == -1 {
				log.Println("Closing socket: client is going away")
				return
			}
			if s.Err != nil {
				log.Printf("An error was sent in the socket event: %s", s.Err.Error())
				// return?
			}
			log.Printf("The event sent was: %s. Listening for next event...", string(s.Message))
			s.Listen()

		default:
			if _, found := h.Cache.Get(infra3ResourceUUID); found {
				h.Cache.Delete(infra3ResourceUUID)

				for _, log := range logs(h.DB, infra3ResourceUUID, generation) {
					b, err := json.Marshal(log)
					if err != nil {
						return
					}
					conn.WriteMessage(1, b)
				}
			}
			time.Sleep(1 * time.Second)
		}
	}

}

type TaskLog struct {
	TaskType  string    `json:"task_type"`
	UUID      string    `json:"uuid"`
	Message   string    `json:"message"`
	Rerun     int       `json:"rerun"`
	TaskID    int       `json:"task_id"`
	UpdatedAt time.Time `json:"updated_at"`
	CreatedAt time.Time `json:"created_at"`
}

func logs(db *gorm.DB, infra3ResourceUUID string, generation string) []TaskLog {
	tasks := []models.TaskPod{}
	queryResult := allTasksGeneratedForResource(db, infra3ResourceUUID, generation).Scan(&tasks)
	if queryResult.Error != nil {
		return nil
	}

	taskMap := map[string][]models.TaskPod{}
	for _, item := range tasks {
		if taskMap[item.TaskType] == nil {
			taskMap[item.TaskType] = []models.TaskPod{item}
		} else {
			taskMap[item.TaskType] = append(taskMap[item.TaskType], item)
		}
	}

	filteredData := []models.TaskPod{}
	currentHightestRerun := 0
	for _, taskType := range taskTypesInOrder {
		if tasks, found := taskMap[taskType]; found {
			indexOfHighestRerun := -1
			for idx, task := range tasks {
				if task.Rerun >= currentHightestRerun {
					currentHightestRerun = task.Rerun
					indexOfHighestRerun = idx
				}
			}
			if indexOfHighestRerun > -1 {
				filteredData = append(filteredData, tasks[indexOfHighestRerun])
			}
		}
	}

	var logs []TaskLog
	for _, task := range filteredData {
		log := struct {
			Message   string
			CreatedAt time.Time
			UpdatedAt time.Time
		}{}
		if result := resourceLog(db, task.UUID).Scan(&log); result.Error == nil {
			taskID := -1
			switch task.TaskType {
			case "setup":
				taskID = 0
			case "preinit":
				taskID = 1
			case "init":
				taskID = 2
			case "postinit":
				taskID = 3
			case "preplan":
				taskID = 4
			case "plan":
				taskID = 5
			case "postplan":
				taskID = 6
			case "preapply":
				taskID = 7
			case "apply":
				taskID = 8
			case "postapply":
				taskID = 9
			case "setup-delete":
				taskID = 10
			case "preinit-delete":
				taskID = 11
			case "init-delete":
				taskID = 12
			case "postinit-delete":
				taskID = 13
			case "preplan-delete":
				taskID = 14
			case "plan-delete":
				taskID = 15
			case "postplan-delete":
				taskID = 16
			case "preapply-delete":
				taskID = 17
			case "apply-delete":
				taskID = 18
			case "postapply-delete":
				taskID = 19
			default:
				taskID = 20
			}
			logs = append(logs, TaskLog{
				UUID:      task.UUID,
				Message:   log.Message,
				TaskType:  task.TaskType,
				Rerun:     task.Rerun,
				TaskID:    taskID,
				CreatedAt: log.CreatedAt,
				UpdatedAt: log.UpdatedAt,
			})
		}
	}
	return logs
}

// Given some human readable data, getWorkflowInfo queries the database and aggregates data relevant
// at the moment of querying. Queries span multiple tables over a few lookups.
// As the function progresses, more data is added to the response. In between lookups, if no data is found
// for a particular query, return all data that has been previously gathered.
func (h APIHandler) getWorkflowInfo(c *gin.Context) {
	name := c.Param("name")
	namespace := c.Param("namespace")
	clusterName := c.Param("cluster_name")
	generation := c.Param("generation")
	clusterID := h.getClusterID(clusterName)
	if clusterID == 0 {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, fmt.Sprintf("cluster_name '%s' not found", clusterName), nil))
		return
	}

	var infra3ResourcesData []struct {
		Name              string    `json:"name"`
		Namespace         string    `json:"namespace"`
		ClusterName       string    `json:"cluster_name"`
		CurrentState      string    `json:"state"`
		UUID              string    `json:"uuid"`
		CurrentGeneration string    `json:"current_generation"`
		UpdatedAt         time.Time `json:"updated_at"`
		CreatedAt         time.Time `json:"created_at"`
	}
	var infra3ResourceSpecsData []struct {
		ResourceSpec string    `json:"resource_spec"`
		Annotations  string    `json:"annotations"`
		Labels       string    `json:"labels"`
		UpdatedAt    time.Time `json:"updated_at"`
		CreatedAt    time.Time `json:"created_at"`
	}
	type task struct {
		UUID                string    `json:"uuid"`
		TaskType            string    `json:"task_type"`
		Rerun               int       `json:"rerun"`
		InClusterGeneration string    `json:"in_cluster_generation"`
		CreatedAt           time.Time `json:"created_at"`
		UpdatedAt           time.Time `json:"updated_at"`
	}
	var tasks []task
	var approvals []struct {
		CreatedAt   time.Time `json:"created_at"`
		UpdatedAt   time.Time `json:"updated_at"`
		IsApproved  bool      `json:"is_approved"`
		TaskPodUUID string    `json:"task_pod_uuid"`
	}

	type ResponseItem struct {
		Name                  string    `json:"name"`
		Namespace             string    `json:"namespace"`
		ClusterName           string    `json:"cluster_name"`
		CurrentState          string    `json:"state"`
		UUID                  string    `json:"uuid"`
		QueryGeneration       string    `json:"query_generation"`
		CurrentGeneration     string    `json:"current_generation"`
		ResourceSpec          string    `json:"resource_spec"`
		Annotations           string    `json:"annotations"`
		Labels                string    `json:"labels"`
		UpdatedAt             time.Time `json:"updated_at"`
		CreatedAt             time.Time `json:"created_at"`
		ResourceSpecCreatedAt time.Time `json:"resource_spec_created_at"`
		ResourceSpecUpdatedAt time.Time `json:"resource_spec_updated_at"`

		Tasks      []task `json:"tasks"`
		IsApproved *bool  `json:"is_approved"`
	}
	finalResult := [1]ResponseItem{}

	queryResult := workflow(h.DB, clusterID, namespace, name).Scan(&infra3ResourcesData)
	if queryResult.Error != nil {
		c.JSON(http.StatusNotFound, response(http.StatusNotFound, "Workflow Query Error: "+queryResult.Error.Error(), []any{}))
		return
	}
	if len(infra3ResourcesData) == 0 {
		c.JSON(http.StatusOK, response(http.StatusOK, "Resource not found", []any{}))
		return
	}

	resourceUUID := infra3ResourcesData[0].UUID
	if generation == "latest" {
		generation = infra3ResourcesData[0].CurrentGeneration
	}

	finalResult[0].Name = infra3ResourcesData[0].Name
	finalResult[0].Namespace = infra3ResourcesData[0].Namespace
	finalResult[0].CurrentState = infra3ResourcesData[0].CurrentState
	finalResult[0].ClusterName = infra3ResourcesData[0].ClusterName
	finalResult[0].CurrentGeneration = infra3ResourcesData[0].CurrentGeneration
	finalResult[0].QueryGeneration = generation
	finalResult[0].UUID = resourceUUID
	finalResult[0].CreatedAt = infra3ResourcesData[0].CreatedAt
	finalResult[0].UpdatedAt = infra3ResourcesData[0].UpdatedAt

	queryResult = resourceSpec(h.DB, resourceUUID, generation).Scan(&infra3ResourceSpecsData)
	if queryResult.Error != nil {
		c.JSON(http.StatusNotFound, response(http.StatusNotFound, "ResourceSpec Query Error: "+queryResult.Error.Error(), []any{}))
		return
	}
	if len(infra3ResourceSpecsData) == 0 {
		c.JSON(http.StatusOK, response(http.StatusOK, "resource spec not found", finalResult))
		return
	}

	finalResult[0].ResourceSpec = infra3ResourceSpecsData[0].ResourceSpec
	finalResult[0].Annotations = infra3ResourceSpecsData[0].Annotations
	finalResult[0].Labels = infra3ResourceSpecsData[0].Labels
	finalResult[0].ResourceSpecCreatedAt = infra3ResourceSpecsData[0].CreatedAt
	finalResult[0].ResourceSpecUpdatedAt = infra3ResourceSpecsData[0].UpdatedAt

	queryResult = allTasksGeneratedForResource(h.DB, resourceUUID, generation).Scan(&tasks)
	if queryResult.Error != nil {
		c.JSON(http.StatusNotFound, response(http.StatusNotFound, "AllTasksGeneratedForResource Query Error: "+queryResult.Error.Error(), []any{}))
		return
	}
	if len(tasks) == 0 {
		c.JSON(http.StatusOK, response(http.StatusOK, "tasks not found", finalResult))
		return
	}

	taskMap := map[string][]task{}
	for _, item := range tasks {
		if taskMap[item.TaskType] == nil {
			taskMap[item.TaskType] = []task{item}
		} else {
			taskMap[item.TaskType] = append(taskMap[item.TaskType], item)
		}
	}

	filteredData := []task{}
	currentHightestRerun := 0
	for _, taskType := range taskTypesInOrder {
		if tasks, found := taskMap[taskType]; found {
			indexOfHightestRerun := 0
			for idx, task := range tasks {
				if task.Rerun >= currentHightestRerun {
					currentHightestRerun = task.Rerun
					indexOfHightestRerun = idx
				}
			}
			filteredData = append(filteredData, tasks[indexOfHightestRerun])
		}
	}

	finalResult[0].Tasks = filteredData

	queryResult = approvalStatusBasedOnLastestRerunOfResource(h.DB, resourceUUID, generation).Scan(&approvals)
	if queryResult.Error != nil {
		c.JSON(http.StatusNotFound, response(http.StatusNotFound, "ApprovalStatusBasedOnLatestRerunOfResource Query Error: "+queryResult.Error.Error(), []any{}))
		return
	}
	if len(approvals) == 0 {
		finalResult[0].IsApproved = nil
		c.JSON(http.StatusOK, response(http.StatusOK, "", finalResult))
		return
	}

	finalResult[0].IsApproved = &approvals[0].IsApproved
	c.JSON(http.StatusOK, response(http.StatusOK, "responseMsg", finalResult))
}

func (h APIHandler) getAllTasksGeneratedForResource(c *gin.Context) {
	resourceUUID := c.Param("infra3_resource_uuid")
	generation := c.Param("generation")

	var result []struct {
		UUID                string `json:"uuid"`
		TaskType            string `json:"task_type"`
		Rerun               int    `json:"rerun"`
		InClusterGeneration string `json:"in_cluster_generation"`
	}

	allTasksGeneratedForResource(h.DB, resourceUUID, generation).Scan(&result)
	c.JSON(http.StatusOK, response(http.StatusOK, "", result))
}

func Reverse[T any](input []T) []T {
	inputLen := len(input)
	output := make([]T, inputLen)

	for i, n := range input {
		j := inputLen - i - 1

		output[j] = n
	}

	return output
}

// Runs the same query as getAllTasksGeneratedForResource but filters tasks based on rerun
func (h APIHandler) getHighestRerunOfTasksGeneratedForResource(c *gin.Context) {
	resourceUUID := c.Param("infra3_resource_uuid")
	generation := c.Param("generation")

	type data struct {
		UUID                string `json:"uuid"`
		TaskType            string `json:"task_type"`
		Rerun               int    `json:"rerun"`
		InClusterGeneration string `json:"in_cluster_generation"`
	}

	var result []data

	allTasksGeneratedForResource(h.DB, resourceUUID, generation).Scan(&result)

	taskMap := map[string][]data{}
	for _, item := range result {
		if taskMap[item.TaskType] == nil {
			taskMap[item.TaskType] = []data{item}
		} else {
			taskMap[item.TaskType] = append(taskMap[item.TaskType], item)
		}
	}

	filteredData := []data{}
	currentHightestRerun := 0
	for _, taskType := range taskTypesInOrder {
		if tasks, found := taskMap[taskType]; found {
			indexOfHightestRerun := 0
			for idx, task := range tasks {
				if task.Rerun >= currentHightestRerun {
					currentHightestRerun = task.Rerun
					indexOfHightestRerun = idx
				}
			}
			filteredData = append(filteredData, tasks[indexOfHightestRerun])
		}
	}

	c.JSON(http.StatusOK, response(http.StatusOK, "", filteredData))
}

func (h APIHandler) setApprovalForResource(c *gin.Context) {
	resourceUUID := c.Param("infra3_resource_uuid")
	generation := c.Param("generation")

	jsonData := struct {
		Approval bool `json:"approval"`
	}{}
	err := c.BindJSON(&jsonData)
	if err != nil {
		log.Println(err)
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, err.Error(), nil))
		return
	}

	var podUUID string
	result := requiredApprovalPodUUID(h.DB, resourceUUID, generation).Scan(&podUUID)
	if result.Error != nil {
		c.JSON(http.StatusNotFound, response(http.StatusNotFound, result.Error.Error(), []any{}))
		return
	}

	approval := models.Approval{
		IsApproved:  jsonData.Approval,
		TaskPodUUID: podUUID,
	}

	createResult := h.DB.Create(&approval)
	if createResult.Error != nil {
		c.JSON(http.StatusBadRequest, response(http.StatusBadRequest, createResult.Error.Error(), []any{}))
		return
	}

	c.JSON(http.StatusNoContent, nil)

}

func (h APIHandler) getApprovalStatusForResource(c *gin.Context) {
	resourceUUID := c.Param("infra3_resource_uuid")
	generation := c.Param("generation")

	var data []struct {
		IsApproved  bool   `json:"is_approved"`
		TaskPodUUID string `json:"task_pod_uuid"`
	}

	queryResult := approvalStatusBasedOnLastestRerunOfResource(h.DB, resourceUUID, generation).Scan(&data)
	if queryResult.Error != nil {
		c.JSON(http.StatusNotFound, response(http.StatusNotFound, queryResult.Error.Error(), []any{}))
		return
	}

	if data == nil {
		c.JSON(http.StatusOK, response(http.StatusOK, "", []any{}))
		return
	}
	c.JSON(http.StatusOK, response(http.StatusOK, "", data))

}

func (h APIHandler) getWorkflowResourceConfiguration(c *gin.Context) {
	resurceUUID := c.Param("infra3_resource_uuid")
	generation := c.Param("generation")

	var result []struct {
		ResourceSpec string `json:"resource_spec"`
		Annotations  string `json:"annotations"`
		Labels       string `json:"labels"`
	}

	resourceSpec(h.DB, resurceUUID, generation).Scan(&result)
	c.JSON(http.StatusOK, response(http.StatusOK, "", result))

}

func (h APIHandler) getWorkflowApprovalStatus(c *gin.Context) {
	taskUUID := c.Param("uuid")

	var result []struct {
		IsApproved bool
	}

	approvalQuery(h.DB, taskUUID).Scan(&result)

	c.JSON(http.StatusOK, response(http.StatusOK, "", result))

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
	infra3Clientset := infra3clientset.NewForConfigOrDie(config)
	clientset := kubernetes.NewForConfigOrDie(config)

	// Before checking for resources to return, check that the current generation has completed
	tf, err := infra3Clientset.Infra3V1().Tfs(namespace).Get(c, name, metav1.GetOptions{})
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

	annotationKey := "infra3-stella.galleybytes.com/sync-upon-completion-of"
	labelSelector := "infra3-stella.galleybytes.com/sync"
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
	secret, err := clientset.CoreV1().Secrets(namespace).Get(context.TODO(), "vc-infra3-virtual-cluster", metav1.GetOptions{})
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
	config.Host = fmt.Sprintf("infra3-virtual-cluster.%s.svc", namespace)
	if VCLUSTER_DEBUG_HOST != "" {
		config.Host = VCLUSTER_DEBUG_HOST
	}
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
			// TODO The addResource returning a msg is unnessesary. Why not redirect to a PUT request instead?
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

	// if infra3Resource.UUID == "" {}
	err = jsonData.validate()
	if err != nil {
		return "", err
	}

	infra3Resource, infra3ResourceSpec, err := jsonData.Parse(clusterID)
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
	result = h.DB.First(&infra3Resource)
	if result.Error == nil {
		// the UUID exists in the database. The result will return a success with the message the resource
		// already exists. To update the resource_spec, the client must make a PUT request instead.
		return fmt.Sprintf("tf resource '%s/%s' already exists", infra3Resource.Namespace, infra3Resource.Name), nil
	} else if result.Error != nil && !errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return "", fmt.Errorf("error occurred when looking for infra3_resource: %v", result.Error)
	}

	result = h.DB.Create(&infra3Resource)
	if result.Error != nil {
		return "", fmt.Errorf("error saving infra3_resource: %s", result.Error)
	}

	err = deleteInfra3ResourcesExceptNewest(h.DB, infra3Resource)
	if err != nil {
		return "", err
	}

	result = h.DB.Create(&infra3ResourceSpec)
	if result.Error != nil {
		return "", fmt.Errorf("error saving infra3_resource_spec: %s", result.Error)
	}

	apiURL := GetApiURL(c, h.serviceIP)
	_, err = NewTaskToken(h.DB, *infra3ResourceSpec, h.tenant, clusterName, apiURL, h.clientset)
	if err != nil {
		return "", err
	}
	appendClusterNameLabel(&jsonData.Tf, cluster.Name)
	addGlobalTaskOptions(&jsonData.Tf, h.tenant, clusterName, apiURL)

	err = applyOnCreateOrUpdate(c, jsonData.Tf, h.clientset, h.tenant, h.fswatchImage)
	if err != nil {
		return "", err
	}

	return "", nil
}

// updateResource updates the resource in the vcluster and the database
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

	// if infra3Resource.UUID == "" {}
	err = jsonData.validate()
	if err != nil {
		return err
	}

	infra3Resource, infra3ResourceSpec, err := jsonData.Parse(clusterID)
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

	// looking for the resource in the database to update
	infra3ResourceFromDatabase := models.Infra3Resource{}
	result = h.DB.Where("uuid = ?", infra3Resource.UUID).First(&infra3ResourceFromDatabase)
	if result.Error != nil {
		// result must exist to update
		return fmt.Errorf("error getting infra3Resource: %v", result.Error)
	}

	// Generation lookups are done from the origin resource and not the generation in the vcluster.
	gen1 := infra3Resource.CurrentGeneration
	gen2 := infra3ResourceFromDatabase.CurrentGeneration
	if compare(gen1, "<", gen2) {
		return fmt.Errorf("error updating resource, generation '%s' is less than current generation '%s'", gen1, gen2)
	}

	if compare(gen1, ">", gen2) {
		// Reset the state of the resource because this is a new generation of the resource spec
		infra3ResourceFromDatabase.CurrentState = models.Untracked
		infra3ResourceFromDatabase.CurrentGeneration = infra3Resource.CurrentGeneration
	}

	result = h.DB.Save(&infra3ResourceFromDatabase) // Updates database state with any generation changes
	if result.Error != nil {
		return result.Error
	}

	err = deleteInfra3ResourcesExceptNewest(h.DB, &infra3ResourceFromDatabase)
	if err != nil {
		return err
	}

	infra3ResourceSpecFromDatabase := models.Infra3ResourceSpec{}
	result = h.DB.Where("infra3_resource_uuid = ? AND generation = ?", infra3ResourceFromDatabase.UUID, infra3ResourceFromDatabase.CurrentGeneration).First(&infra3ResourceSpecFromDatabase)
	if result.Error != nil && errors.Is(result.Error, gorm.ErrRecordNotFound) {
		result = h.DB.Create(&infra3ResourceSpec)
		if result.Error != nil {
			return result.Error
		}
		infra3ResourceSpecFromDatabase = *infra3ResourceSpec
	} else if result.Error != nil {
		return fmt.Errorf("error occurred when looking for infra3_resource_spec: %v", result.Error)
	}

	apiURL := GetApiURL(c, h.serviceIP)
	_, err = NewTaskToken(h.DB, infra3ResourceSpecFromDatabase, h.tenant, clusterName, apiURL, h.clientset)
	if err != nil {
		return err
	}
	appendClusterNameLabel(&jsonData.Tf, clusterName)
	addGlobalTaskOptions(&jsonData.Tf, h.tenant, clusterName, apiURL)

	err = applyOnCreateOrUpdate(c, jsonData.Tf, h.clientset, h.tenant, h.fswatchImage)
	if err != nil {
		return err
	}

	return nil
}

// manualTokenPatch is used to re-submit a new token secret to the vcluster. Resources can generally
// use a refresh token, but this can be useful if the refresh token has been invalidated.
func (h APIHandler) manualTokenPatch(c *gin.Context) {
	name := c.Param("name")
	namespace := c.Param("namespace")
	clusterName := c.Param("cluster_name")

	var infra3ResourceSpec models.Infra3ResourceSpec

	result := h.DB.Raw(`
		SELECT
			infra3_resource_specs.*
		FROM
			infra3_resource_specs
			JOIN
				infra3_resources
				ON infra3_resources.uuid = infra3_resource_specs.infra3_resource_uuid
			JOIN
				clusters
				ON clusters.id = infra3_resources.cluster_id
		WHERE clusters.name = ?
			AND infra3_resources.namespace = ?
			AND infra3_resources.name = ?
			AND infra3_resource_specs.generation = infra3_resources.current_generation
	`, clusterName, namespace, name).Scan(&infra3ResourceSpec)
	if result.Error != nil {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, fmt.Sprintf("error getting TFOResourceSpec: %v", result.Error), nil))
		return
	}
	if infra3ResourceSpec.ID == 0 {
		c.JSON(http.StatusNotFound, response(http.StatusNotFound, "TFOResourceSpec not found", nil))
		return
	}

	apiURL := GetApiURL(c, h.serviceIP)
	_, err := NewTaskToken(h.DB, infra3ResourceSpec, h.tenant, clusterName, apiURL, h.clientset)
	if err != nil {

		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, err.Error(), nil))
		return
	}

	c.JSON(http.StatusNoContent, response(http.StatusNoContent, "", nil))
}

// Soft deletes infra3_resources from the database except the latest one via created_at timestamp
func deleteInfra3ResourcesExceptNewest(db *gorm.DB, infra3Resource *models.Infra3Resource) error {
	// A infra3 resource has a namespace and a name, which are used to identify it uniquely within the cluster.
	// Upon the successful creation of a new infra3Resource, any previous infra3Resources matching the
	// clusterID/namespace/name must be "deleted" by adding a "deleted_at" timestamp.
	var infra3Resources []models.Infra3Resource
	result := db.Where("name = ? AND namespace = ? AND cluster_id = ?", infra3Resource.Name, infra3Resource.Namespace, infra3Resource.ClusterID).Order("created_at desc").Offset(1).Find(&infra3Resources)
	if result.Error == nil {
		for i, _ := range infra3Resources {
			infra3Resources[i].DeletedBy = infra3Resource.UUID
		}
		if len(infra3Resources) > 0 {
			result = db.Save(&infra3Resources)
			if result.Error != nil {
				return fmt.Errorf("error writing to infra3_resources: %s", result.Error)
			}

			result = db.Delete(&infra3Resources)
			if result.Error != nil {
				return fmt.Errorf("error (soft) deleting infra3_resources: %s", result.Error)
			}
		}

	}
	return nil
}

// Generate a new JWT Token and Refresh Token. Saves the token into the database and creats a Secret in the
// tasks namespace inside the vcluster. The secrets is to be used with envFrom inside the task pods.
//
// The generated name is the resourceName + "-jwt".
func NewTaskToken(db *gorm.DB, infra3ResourceSpec models.Infra3ResourceSpec, _tenantID, clusterName, apiURL string, clientset kubernetes.Interface) (*string, error) {
	// tenantID is totally broken right now. Just hardcode this for now
	tenantID := "internal"
	var token string
	var infra3Resource models.Infra3Resource
	var version int
	infra3ResourceSpecID := infra3ResourceSpec.ID
	generation := infra3ResourceSpec.Generation
	resourceUUID := infra3ResourceSpec.Infra3ResourceUUID

	token, refreshToken, err := generateTaskJWT(resourceUUID, tenantID, clusterName, generation)
	if err != nil {
		// TODO
		//    TFO-API should make having a valid token part of saving to the cluster.
		//    Then the execution of the terraform should use the token to validate before running.
		return nil, fmt.Errorf("failed to generate taskJWT: %s", err)

	} else {

		result := db.Raw("SELECT * FROM infra3_resources WHERE uuid = ?", infra3ResourceSpec.Infra3ResourceUUID).Scan(&infra3Resource)
		if result.Error != nil {
			return nil, fmt.Errorf("failed to find TFO Resource: %s", result.Error)
		}

		_ = db.Table("refresh_tokens").Select("max(version)").Where("infra3_resource_spec_id = ?", infra3ResourceSpecID).Scan(&version)
		if result.Error != nil {
			if !errors.Is(result.Error, gorm.ErrModelValueRequired) && !errors.Is(result.Error, gorm.ErrRecordNotFound) {
				return nil, fmt.Errorf("failed to get previous token version: %s", result.Error)
			}
		}

		validatedRefreshtoken, err := doValidation(refreshToken)
		if err != nil {
			return nil, fmt.Errorf("failed to validate token: %s", err)
		}

		hash, err := util.HashPassword(validatedRefreshtoken.Signature)
		if err != nil {
			return nil, fmt.Errorf("failed to hash token for storage: %s", err)
		}

		config, err := getVclusterConfig(clientset, tenantID, clusterName)
		if err != nil {
			return nil, fmt.Errorf("error occurred getting vcluster config for %s-%s %s/%s: %s", tenantID, clusterName, infra3Resource.Namespace, infra3Resource.Name, err)
		}
		vclusterClient := kubernetes.NewForConfigOrDie(config)

		// Try and create the namespace for the tfResource. Acceptable error is if namespace already exists.
		_, err = vclusterClient.CoreV1().Namespaces().Create(context.TODO(), &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: infra3Resource.Namespace,
			},
		}, metav1.CreateOptions{})
		if err != nil {
			if !kerrors.IsAlreadyExists(err) {
				return nil, fmt.Errorf("namespace/%s could not be created in vcluster: %s", infra3Resource.Namespace, err)
			}
		}

		// generate a secret manifest for the jwt token
		secretName := util.Trunc(infra3Resource.Name, 249) + "-jwt"
		secretConfig := corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: infra3Resource.Namespace,

				// The OwnerReference is not working as expected and the secret is getting removed
				// immediately after it's creation. Find out the right way to add ownership
				// so the secrets have the same lifetime as the resource that consumes it.

				/* ************************************************************

				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion:         string("tf.galleybytes.com/v1beta1"),
						Kind:               string("Terraform"),
						Name:               infra3Resource.Name,
						UID:                types.UID(infra3Resource.UUID),
						Controller:         newTrue(),
						BlockOwnerDeletion: newTrue(),
					},
				},

				************************************************************* */
			},

			StringData: map[string]string{
				"I3_API_LOG_TOKEN": token,
				"REFRESH_TOKEN":     refreshToken,
			},
			Type: corev1.SecretTypeOpaque,
		}

		_, err = vclusterClient.CoreV1().Secrets(infra3Resource.Namespace).Create(context.TODO(), &secretConfig, metav1.CreateOptions{
			FieldManager: "infra3api",
		})
		if err != nil {
			if !kerrors.IsAlreadyExists(err) {
				return nil, fmt.Errorf("failed to create secret: %s", err)
			}
			patch := []byte(fmt.Sprintf(`[
			{"op": "replace", "path": "/data/I3_API_LOG_TOKEN", "value": "%s"},
			{"op": "replace", "path": "/data/REFRESH_TOKEN", "value": "%s"}
		]`, util.B64Encode(token), util.B64Encode(refreshToken)))
			_, err := vclusterClient.CoreV1().Secrets(infra3Resource.Namespace).Patch(context.TODO(), secretName, types.JSONPatchType, patch, metav1.PatchOptions{})
			if err != nil {
				return nil, fmt.Errorf("failed to patch secret: %s", err)
			}
		}
		log.Printf("Patched %s/%s in %s-%s", infra3Resource.Namespace, secretName, tenantID, clusterName)

		refreshToken := models.RefreshToken{
			RefreshToken:         hash,
			Version:              version + 1,
			UsedAt:               nil,
			CanceledAt:           nil,
			ReUsedAt:             nil,
			Infra3ResourceSpecID: infra3ResourceSpec.ID,
		}

		result = db.Exec("UPDATE refresh_tokens SET canceled_at = ?, canceled_reason = 'NEW_TOKEN_GENERATION' WHERE infra3_resource_spec_id = ?", time.Now(), infra3ResourceSpecID)
		if result.Error != nil {
			return nil, fmt.Errorf("failed to invalidate previous tokens: %s", result.Error)
		}

		result = db.Save(&refreshToken)
		if result.Error != nil {
			return nil, fmt.Errorf("failed to save refresh_token to database: %s", result.Error)
		}
	}

	return &token, nil
}

func newTrue() *bool { b := true; return &b }

func NewTaskTokenFromRefreshToken(db *gorm.DB, refreshToken, apiURL string, clientset kubernetes.Interface) (string, error) {
	var data struct {
		models.RefreshToken
		models.Infra3ResourceSpec
		models.Infra3Resource
		models.Cluster
	}

	var signature string
	var infra3OriginUUID string

	parsedToken, err := doValidation(refreshToken)
	if err != nil {
		return "", fmt.Errorf("failed to validate token: %s", err)
	}

	signature = parsedToken.Signature

	jwt.Parse(refreshToken, func(t *jwt.Token) (interface{}, error) {
		claims := t.Claims.(jwt.MapClaims)
		infra3OriginUUID = claims["username"].(string)
		return nil, nil
	})

	if infra3OriginUUID == "" {
		return "", fmt.Errorf("invalid token claims")
	}

	query := fmt.Sprintf(`
		SELECT * FROM refresh_tokens
		JOIN infra3_resource_specs
			ON infra3_resource_specs.id = refresh_tokens.infra3_resource_spec_id
		JOIN infra3_resources
			ON infra3_resources.uuid = infra3_resource_specs.infra3_resource_uuid
		JOIN clusters
			ON clusters.id = infra3_resources.cluster_id
		WHERE infra3_resource_specs.infra3_resource_uuid = '%s'
		AND refresh_tokens.canceled_at IS NULL
		AND refresh_tokens.version = (
			SELECT MAX(version) FROM refresh_tokens
			JOIN infra3_resource_specs
				ON infra3_resource_specs.id = refresh_tokens.infra3_resource_spec_id
			WHERE infra3_resource_specs.infra3_resource_uuid = '%s'
		)
	`, infra3OriginUUID, infra3OriginUUID)

	result := db.Raw(query).Scan(&data)
	if result.Error != nil {
		return "", result.Error
	}

	if !util.CheckPasswordHash(signature, data.RefreshToken.RefreshToken) {
		return "", fmt.Errorf("invalid refresh token")
	}

	token, err := NewTaskToken(db, data.Infra3ResourceSpec, "", data.Cluster.Name, apiURL, clientset)
	if err != nil {
		return "", err
	}

	return *token, nil
}

func GetApiURL(c *gin.Context, serviceIP *string) string {
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
func appendClusterNameLabel(tf *infra3v1.Tf, clusterName string) {
	if clusterName == "" {
		return
	}
	if tf.Labels == nil {
		tf.Labels = map[string]string{}
	}
	tf.Labels["infra3-stella.galleybytes.com/cluster-name"] = clusterName
}

// addGlobalTaskOptions will inject I3_ORIGIN envs to the incoming resource
func addGlobalTaskOptions(tf *infra3v1.Tf, tenant, clusterName, apiURL string) {
	secretName := util.Trunc(tf.Name, 249) + "-jwt"
	generation := fmt.Sprintf("%d", tf.Generation)
	resourceUUID := string(tf.UID)
	tf.Spec.TaskOptions = append(tf.Spec.TaskOptions, infra3v1.TaskOption{
		For: []infra3v1.TaskName{"*"},
		Volumes: []corev1.Volume{
			{
				Name: "jwt",
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{
						SecretName: secretName,
					},
				},
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      "jwt",
				ReadOnly:  true,
				MountPath: "/jwt",
			},
		},
		EnvFrom: []corev1.EnvFromSource{
			{
				SecretRef: &corev1.SecretEnvSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: secretName,
					},
					Optional: new(bool),
				},
			},
		},
		Env: []corev1.EnvVar{
			{
				Name:  "I3_ORIGIN_UUID",
				Value: resourceUUID,
			},
			{
				Name:  "I3_ORIGIN_GENERATION",
				Value: generation,
			},
			{
				Name:  "I3_API_URL",
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

func applyOnCreateOrUpdate(ctx context.Context, tf infra3v1.Tf, clientset kubernetes.Interface, _tenantID, fswatchImage string) error {
	// tenantID is totally broken right now. Just hardcode this for now
	tenantID := "internal"

	labelKey := "infra3-stella.galleybytes.com/cluster-name"
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
	vclusterInfra3Client := infra3clientset.NewForConfigOrDie(config)

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
	tfs, err := vclusterInfra3Client.Infra3V1().Tfs(tf.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("error occurred listing tf objects in vcluster: %s", err)
	}
	isPatch := false // True when terraform resource exists
	for _, item := range tfs.Items {
		if item.Name == tf.Name {
			tf.SetManagedFields(item.GetManagedFields())
			tf.SetOwnerReferences(item.GetOwnerReferences())
			tf.SetDeletionGracePeriodSeconds(item.GetDeletionGracePeriodSeconds())
			tf.SetDeletionTimestamp(item.GetDeletionTimestamp())
			tf.SetFinalizers(item.GetFinalizers())
			tf.SetGenerateName(item.GetGenerateName())
			tf.SetResourceVersion(item.GetResourceVersion())
			tf.SetUID(item.GetUID())
			tf.SetSelfLink(item.GetSelfLink())
			tf.SetGeneration(item.GetGeneration())
			tf.SetManagedFields(item.GetManagedFields())
			tf.SetCreationTimestamp(item.GetCreationTimestamp())
			tf.Status = item.Status
			isPatch = true
			break
		}
	}

	fswatchImageConfig := infra3v1.ImageConfig{
		Image:           fswatchImage,
		ImagePullPolicy: corev1.PullIfNotPresent,
	}
	for _, taskName := range []infra3v1.TaskName{
		infra3v1.RunSetup,
		infra3v1.RunPreInit,
		infra3v1.RunInit,
		infra3v1.RunPostInit,
		infra3v1.RunPrePlan,
		infra3v1.RunPlan,
		infra3v1.RunPostPlan,
		infra3v1.RunPreApply,
		infra3v1.RunApply,
		infra3v1.RunPostApply,
		infra3v1.RunSetupDelete,
		infra3v1.RunPreInitDelete,
		infra3v1.RunInitDelete,
		infra3v1.RunPostInitDelete,
		infra3v1.RunPrePlanDelete,
		infra3v1.RunPlanDelete,
		infra3v1.RunPostPlanDelete,
		infra3v1.RunPreApplyDelete,
		infra3v1.RunApplyDelete,
		infra3v1.RunPostApplyDelete} {
		addSidecar(&tf, taskName+"-fswatch", fswatchImageConfig, taskName, nil)
	}

	addTaskOption(&tf, infra3v1.TaskOption{
		For: []infra3v1.TaskName{"*"},
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
		err = doPatch(&tf, ctx, tf.Name, tf.Namespace, vclusterInfra3Client)
		if err != nil {
			return fmt.Errorf("error occurred patching tf object: %v", err)
			// continue
		}
		log.Printf("Successfully patched %s-%s %s/%s", tenantID, clusterName, tf.Namespace, tf.Name)
		return nil
	}

	err = doCreate(tf, ctx, tf.Namespace, vclusterInfra3Client)
	log.Printf("Creating %s-%s %s/%s", tenantID, clusterName, tf.Namespace, tf.Name)
	if err != nil {
		return fmt.Errorf("error creating new tf resource: %v", err)
	}
	log.Printf("Successfully created %s-%s %s/%s", tenantID, clusterName, tf.Namespace, tf.Name)
	return nil
}

func addSidecar(tf *infra3v1.Tf, name infra3v1.TaskName, imageConfig infra3v1.ImageConfig, task infra3v1.TaskName, taskOption *infra3v1.TaskOption) {
	if tf.Spec.Plugins == nil {
		tf.Spec.Plugins = make(map[infra3v1.TaskName]infra3v1.Plugin)
	}
	for key := range tf.Spec.Plugins {
		if key == name {
			return // do not add duplicates
		}
	}

	tf.Spec.Plugins[name] = infra3v1.Plugin{
		ImageConfig: imageConfig,
		When:        "Sidecar",
		Task:        task,
		Must:        true,
	}

	if taskOption != nil {
		tf.Spec.TaskOptions = append(tf.Spec.TaskOptions, *taskOption)
	}
}

func addTaskOption(tf *infra3v1.Tf, taskOption infra3v1.TaskOption) {
	tf.Spec.TaskOptions = append(tf.Spec.TaskOptions, taskOption)
}

func patchableTFResource(obj *infra3v1.Tf) []byte {
	gvks, _, err := scheme.Scheme.ObjectKinds(obj)
	if err != nil || len(gvks) == 0 {
		obj.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   infra3v1.SchemeGroupVersion.Group,
			Version: infra3v1.SchemeGroupVersion.Version,
			Kind:    "Terraform",
		})
	}

	buf := bytes.NewBuffer([]byte{})
	k8sjson.NewSerializer(k8sjson.DefaultMetaFactory, runtime.NewScheme(), runtime.NewScheme(), true).Encode(obj, buf)
	return buf.Bytes()
}

func doCreate(new infra3v1.Tf, ctx context.Context, namespace string, client infra3clientset.Interface) error {
	_, err := client.Infra3V1().Tfs(namespace).Create(ctx, &new, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("an error occurred saving tf object: %v", err)
	}
	return nil
}

func doPatch(tf *infra3v1.Tf, ctx context.Context, name, namespace string, client infra3clientset.Interface) error {
	_, err := client.Infra3V1().Tfs(namespace).Update(ctx, tf, metav1.UpdateOptions{})
	if err != nil {
		return err
	}

	return nil
}
