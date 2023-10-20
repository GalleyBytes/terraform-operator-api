package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	ptylib "github.com/creack/pty"
	"github.com/galleybytes/terraform-operator-api/pkg/common/models"
	tfv1beta1 "github.com/galleybytes/terraform-operator/pkg/apis/tf/v1beta1"
	tfo "github.com/galleybytes/terraform-operator/pkg/client/clientset/versioned"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/sorenisanerd/gotty/webtty"
	"gopkg.in/yaml.v3"
	"gorm.io/gorm"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/kubectl/pkg/cmd/exec"
	"k8s.io/kubectl/pkg/scheme"
)

type Response struct {
	StatusInfo StatusInfo  `json:"status_info"`
	Data       interface{} `json:"data"`
}

type StatusInfo struct {
	StatusCode int64  `json:"status_code"`
	Message    string `json:"message"`
}

func response(httpstatus int64, message string, results interface{}) *Response {
	resp := Response{
		StatusInfo: StatusInfo{
			StatusCode: httpstatus,
			Message:    message,
		},
		Data: results,
	}
	return &resp
}

func (h APIHandler) GetDistinctGeneration(c *gin.Context) {
	// The TFOResourceSpec is created once for each generation that passes thru the monitor. It is the best
	// resource to query for generations of a particular resource.
	uuid := c.Param("tfo_resource_uuid")
	var generation []int
	if result := h.DB.Raw("SELECT DISTINCT generation FROM tfo_resource_specs WHERE tfo_resource_uuid = ?", &uuid).Scan(&generation); result.Error != nil {
		c.AbortWithError(http.StatusNotFound, result.Error)
		return
	}
	c.JSON(http.StatusOK, &generation)
}

func (h APIHandler) GetUuidByClusterID(c *gin.Context) {
	clusterID := c.Param("cluster_id")
	var clusterIdInfo models.TFOResource

	if result := h.DB.Where("cluster_id = ?", clusterID).First(&clusterIdInfo); result.Error != nil {
		c.AbortWithError(http.StatusNotFound, result.Error)
		return
	}

	c.JSON(http.StatusOK, &clusterIdInfo)

}

func (h APIHandler) GetCluster(c *gin.Context) {
	clusterID := c.Param("cluster_id")
	if clusterID == "" {
		// ID must be integer-like
		clusterID = "-1" // will hopefully never match a primary key
	}
	clusterName := c.Param("cluster_name")
	var clusters []models.Cluster
	responseMsg := ""
	if result := h.DB.Where("name = ?", clusterName).Or("id = ?", clusterID).First(&clusters); result.Error != nil {
		if !errors.Is(result.Error, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, result.Error.Error(), clusters))
			return
		}
		responseMsg = result.Error.Error()
	}

	c.JSON(http.StatusOK, response(http.StatusOK, responseMsg, clusters))

}

func (h APIHandler) Index(c *gin.Context) {
	// TODO return api discovery data
	c.JSON(http.StatusNoContent, nil)
}

func (h APIHandler) workflows(c *gin.Context) {
	matchAny, _ := c.GetQuery("matchAny")
	offset, _ := c.GetQuery("offset")
	limit, _ := c.GetQuery("limit")
	n, _ := strconv.Atoi(offset)
	l, _ := strconv.Atoi(limit)

	if l == 0 {
		l = 10
	}

	var result []struct {
		Name              string `json:"name"`
		Namespace         string `json:"namespace"`
		ClusterName       string `json:"cluster_name"`
		CurrentState      string `json:"state"`
		UUID              string `json:"uuid"`
		CurrentGeneration string `json:"current_generation"`
	}

	query := workflows(h.DB).
		Limit(l).
		Offset(n)

	if matchAny != "" {
		m := fmt.Sprintf("%%%s%%", matchAny)
		name := m
		namespace := m
		clusterName := m

		if strings.Contains(matchAny, "=") {
			name = "%"
			namespace = "%"
			clusterName = "%"
			for _, matchAnyOfColumn := range strings.Split(matchAny, " ") {
				if !strings.Contains(matchAnyOfColumn, "=") {
					continue
				}
				columnQuery := strings.Split(matchAnyOfColumn, "=")
				key := columnQuery[0]
				value := columnQuery[1]
				if key == "name" {
					query.Where("tfo_resources.name LIKE ?", fmt.Sprintf("%%%s%%", value))
				}
				if key == "namespace" {
					query.Where("tfo_resources.namespace LIKE ?", fmt.Sprintf("%%%s%%", value))
				}
				if strings.HasPrefix(key, "cluster") {
					query.Where("clusters.name LIKE ?", fmt.Sprintf("%%%s%%", value))
				}
			}
		} else {
			query.Where("(tfo_resources.name LIKE ? or tfo_resources.namespace LIKE ? or clusters.name LIKE ?)",
				name,
				namespace,
				clusterName,
			)
		}

	}

	query.Scan(&result)

	c.JSON(http.StatusOK, response(http.StatusOK, "", result))
}

func (h APIHandler) ListClusters(c *gin.Context) {
	var clusters []models.Cluster

	if result := h.DB.Find(&clusters); result.Error != nil {
		c.AbortWithError(http.StatusNotFound, result.Error)
		return
	}

	c.JSON(http.StatusOK, &clusters)
}

func (h APIHandler) GetClustersResources(c *gin.Context) {
	var resources []models.TFOResource
	clusterID := c.Param("cluster_id")

	if result := h.DB.Where("cluster_id = ?", clusterID).Find(&resources); result.Error != nil {
		c.AbortWithError(http.StatusNotFound, result.Error)
		return
	}

	c.JSON(http.StatusOK, &resources)
}

func (h APIHandler) GetResourceByUUID(c *gin.Context) {
	var tfoResources []models.TFOResource
	uuid := c.Param("tfo_resource_uuid")
	responseMsg := ""
	if result := h.DB.First(&tfoResources, "uuid = ?", uuid); result.Error != nil {
		if !errors.Is(result.Error, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, result.Error.Error(), tfoResources))
			return
		}
		responseMsg = result.Error.Error()
	}

	c.JSON(http.StatusOK, response(http.StatusOK, responseMsg, tfoResources))
}

func highestRerun(taskPods []models.TaskPod, taskType string, minimum float64) (models.TaskPod, float64) {
	taskPodOfHighestRerun := models.TaskPod{}
	highestRerunObservedInLogs := 0
	for _, taskPod := range taskPods {
		if taskPod.TaskType == taskType {
			if taskPod.Rerun > highestRerunObservedInLogs {
				highestRerunObservedInLogs = taskPod.Rerun
			}
		}
	}

	// return only the highest rerun. It is ok to return an empty list, this just indicates that the task
	// has not produced logs yet.
	rerun := math.Max(float64(highestRerunObservedInLogs), minimum)
	for _, taskPod := range taskPods {
		if taskPod.TaskType == taskType && taskPod.Rerun == int(rerun) {
			taskPodOfHighestRerun = taskPod
		}
	}
	return taskPodOfHighestRerun, rerun
}

func (h APIHandler) LatestGeneration(uuid string) string {
	var tfoResource models.TFOResource
	if result := h.DB.First(&tfoResource, "uuid = ?", &uuid); result.Error != nil {
		return ""
	}
	return tfoResource.CurrentGeneration
}

// ResourceLog data contract for clients to consume
type ResourceLog struct {
	ID              uint   `json:"id"`
	LogMessage      string `json:"message"`
	TaskType        string `json:"task_type"`
	Rerun           int    `json:"rerun"`
	LineNo          string `json:"line_no"`
	TFOResourceUUID string `json:"tfo_resource_uuid"`
}

// GetClustersResourceLogs will return the latest logs for the selected resource. The only filted allowed
// in this call is the generation to switch getting the latest logs for a given generation.
func (h APIHandler) GetClustersResourcesLogs(c *gin.Context) {

	// URL param arguments expected. These are used to construct the url and are always expected to contain a string
	generationFilter := c.Param("generation")
	taskTypeFilter := c.Param("task_type")
	rerunFilter := c.Param("rerun")
	uuid := c.Param("tfo_resource_uuid")

	logs, err := h.ResourceLogs(generationFilter, rerunFilter, taskTypeFilter, uuid)
	if err != nil {
		c.AbortWithError(http.StatusNotFound, err)
	}

	c.JSON(http.StatusOK, response(http.StatusOK, "", logs))
}

func (h APIHandler) ResourceLogs(generationFilter, rerunFilter, taskTypeFilter, uuid string) ([]ResourceLog, error) {
	logs := []ResourceLog{}
	if generationFilter == "latest" || generationFilter == "" {
		// "latest" is a special case that reads the 'CurrentGeneration' value out of TFOResource
		generationFilter = h.LatestGeneration(uuid)
	}

	var taskPods []models.TaskPod
	if rerunFilter != "" {
		if result := h.DB.Where("tfo_resource_uuid = ? AND generation = ? AND rerun = ?", &uuid, &generationFilter, &rerunFilter).Find(&taskPods); result.Error != nil {
			return logs, result.Error
		}
	} else {
		if result := h.DB.Where("tfo_resource_uuid = ? AND generation = ?", &uuid, &generationFilter).Find(&taskPods); result.Error != nil {
			return logs, result.Error
		}
	}

	// We've retrieved all the logs for the generation at this point, but we need to ensure we only display
	// the latest rerun logs. If a user wants a specific rerun, a different query will need to be constructed

	// We must have knowledge of the order of logs to return the correct result to the user. Each task type
	// can have a different rerun number, and therefore, we have to make sure that when the results are
	// returned, the latest tasks do not have earlier reruns than previous tasks.
	//
	//    9 |                       |
	//    8 |                       |
	// R  7 |   Good Results        |   Bad result becuase the apply log
	// E  6 |                       |   doesn't reflect the result of plan
	// R  5 |                       |
	// U  4 |                       |             x
	// N  3 |         x   x         |                x
	//    2 |                       |
	//    1 |    x                  |        x
	//    0 |_x_____________________|____x________________________
	//       p   t   n   y               p   t   n   y
	//      u   i   a   l               u   i   a   l
	//     t   n   l   p               t   n   l   p
	//    e   i   p   p               e   i   p   p
	//   s           a               s           a

	// Create this in order to store the logs filtered by the rerun sort
	// filteredResuilts := []models.TFOTaskLog{}

	taskPodsOfHighestRerun := []models.TaskPod{}
	currentRerun := float64(0)
	for _, taskType := range taskTypesInOrder {
		taskPod, rerun := highestRerun(taskPods, taskType, currentRerun)
		currentRerun = rerun
		if taskTypeFilter != "" && taskPod.TaskType != taskTypeFilter {
			continue
		}
		taskPodsOfHighestRerun = append(taskPodsOfHighestRerun, taskPod)
	}

	// Find all the tfoTaskLogs that were created from the taskPods
	taskPodUUIDs := []string{}
	for _, t := range taskPodsOfHighestRerun {
		taskPodUUIDs = append(taskPodUUIDs, t.UUID)
	}
	var tfoTaskLogs []models.TFOTaskLog
	if result := h.DB.Where("task_pod_uuid IN ?", taskPodUUIDs).Find(&tfoTaskLogs); result.Error != nil {
		return logs, result.Error
	}

	// TODO optimize the taskPod/taskLog matching algorithm, leaving this simple lookup since logs are
	// unlikely to be more than just a few thousand lines max. This number should be easily handled.
	for _, taskPod := range taskPodsOfHighestRerun {
		for _, log := range tfoTaskLogs {
			if log.TaskPodUUID == taskPod.UUID {
				// TODO does the size need to be sent?
				logs = append(logs, ResourceLog{
					ID:         log.ID,
					LogMessage: log.Message,
					Rerun:      taskPod.Rerun,
					TaskType:   taskPod.TaskType,
				})
			}
		}
	}
	return logs, nil
}

func (h APIHandler) GetTFOTaskLogsViaTask(c *gin.Context) {
	emptyResponse := []interface{}{}
	taskPodUUID := c.Param("task_pod_uuid")

	var tfoTaskLogs []models.TFOTaskLog
	if result := h.DB.Where("task_pod_uuid = ?", taskPodUUID).Find(&tfoTaskLogs); result.Error != nil {

		// TODO No plans have been executed yet. This is not an error but we are not able to continue until the plan pod shows up.
		c.JSON(http.StatusOK, response(http.StatusOK, "TaskPod "+result.Error.Error(), emptyResponse))
		return
	}

	c.JSON(http.StatusOK, response(http.StatusOK, "", tfoTaskLogs))
}

func (h APIHandler) LookupResourceSpec(generation, uuid string) *models.TFOResourceSpec {
	var tfoResource models.TFOResource
	var tfoResourceSpec models.TFOResourceSpec

	if generation == "latest" {
		if result := h.DB.First(&tfoResource, "uuid = ?", &uuid); result.Error != nil {
			return nil
		}
		generation = tfoResource.CurrentGeneration
	}

	if result := h.DB.Where("tfo_resource_uuid = ? AND generation =?", uuid, generation).First(&tfoResourceSpec); result.Error != nil {
		return nil
	}

	return &tfoResourceSpec
}

type GetResourceSpecResponseData struct {
	models.TFOResourceSpec `json:",inline"`
}

// func (h APIHandler) GetResourceSpec(c *gin.Context) {
// 	uuid := c.Param("tfo_resource_uuid")
// 	generation := c.Param("generation")
// 	tfoResourceSpec := h.LookupResourceSpec(generation, uuid)

// 	responseData := []interface{}{}
// 	if tfoResourceSpec != nil {
// 		responseData = append(responseData, GetResourceSpecResponseData{TFOResourceSpec: *tfoResourceSpec})
// 	}
// 	c.JSON(http.StatusOK, &responseData)
// }

type GetApprovalStatusResponseData struct {
	TFOResourceUUID string `json:"tfo_resource_uuid"`
	TaskPodUUID     string `json:"task_pod_uuid"`

	// Status is fuzzy. -1 means it hasn't been decided, 0 is false, 1 is true for the approvals.
	// Hasn't been decided means there is no record in the approvals table matching the uuid.
	Status int `json:"status"`
}

func (h APIHandler) GetTaskPod(c *gin.Context) {
	responseData := []interface{}{}
	taskPodUUID := c.Param("task_pod_uuid")
	taskPods := []models.TaskPod{}
	if result := h.DB.Where("uuid = ?", &taskPodUUID).Find(&taskPods); result.Error != nil {
		// TODO No plans have been executed yet. This is not an error but we are not able to continue until the plan pod shows up.
		c.JSON(http.StatusOK, response(http.StatusOK, result.Error.Error(), responseData))
		return
	}
	c.JSON(http.StatusOK, response(http.StatusOK, "", taskPods))
}

type approvalResponse struct {
	models.Approval `json:",inline"`
	Status          string `json:"status"`
}

func (h APIHandler) AllApprovals(c *gin.Context) {
	approval := []models.Approval{}
	h.DB.Last(&approval)
	c.JSON(http.StatusOK, response(http.StatusOK, "", approval))
}

func (h APIHandler) GetApprovalStatusViaTaskPodUUID(c *gin.Context) {
	responseData := []interface{}{}
	taskPodUUID := c.Param("task_pod_uuid")
	taskPod := models.TaskPod{}
	if result := h.DB.Where("uuid = ?", &taskPodUUID).Find(&taskPod); result.Error != nil {
		// TODO No plans have been executed yet. This is not an error but we are not able to continue until the plan pod shows up.
		c.JSON(http.StatusOK, response(http.StatusOK, "TaskPod "+result.Error.Error(), responseData))
		return
	}
	if taskPod.TaskType != "plan" {
		// TODO what's the message
		c.JSON(http.StatusOK, response(http.StatusOK, fmt.Sprintf("approvals are for plan types, but uuid was for %s type", taskPod.TaskType), responseData))
		return
	}

	approvals := []models.Approval{}
	if result := h.DB.Where("task_pod_uuid = ?", &taskPod.UUID).First(&approvals); result.Error != nil {
		c.JSON(http.StatusOK, response(http.StatusOK, "Approval "+result.Error.Error(), []approvalResponse{
			{
				Status: "nodata",
				Approval: models.Approval{
					TaskPodUUID: taskPodUUID,
				},
			},
		}))
		return
	}

	c.JSON(http.StatusOK, response(http.StatusOK, "", []approvalResponse{
		{
			Approval: approvals[0],
			Status:   "complete",
		},
	}))
}

// GetApprovalStatus only looks at the latest resource spec by getting the TFOResource's 'LatestGeneration'.
// Use the generation to get the TFOResourceSpec and parses the "spec" for the requireApproval value. If the
// value is "true", this function finds the latest plan task by getting the TaskPod with the highest rerun number.
// The UUID of the TaskPod is used to lookup the Approval status to return to the caller.
func (h APIHandler) GetApprovalStatus(c *gin.Context) {
	responseData := []interface{}{}
	uuid := c.Param("tfo_resource_uuid")

	generationFilter := c.Param("generation")
	generation := generationFilter
	if generationFilter == "" || generationFilter == "latest" {
		generation = h.LatestGeneration(uuid)
	}

	tfoResourceSpec := h.LookupResourceSpec(generation, uuid)
	if tfoResourceSpec == nil {
		// TODO What's the error messsage?
		c.JSON(http.StatusOK, response(http.StatusOK, "", responseData))
		return
	}

	spec := struct {
		RequireApproval bool `yaml:"requireApproval"`
	}{}

	err := yaml.Unmarshal([]byte(tfoResourceSpec.ResourceSpec), &spec)
	if err != nil {
		c.JSON(http.StatusOK, response(http.StatusOK, err.Error(), responseData))
		return
	}

	if !spec.RequireApproval {
		// TODO what's the message when no require approval is required?
		c.JSON(http.StatusOK, response(http.StatusOK, "", responseData))
		return
	}

	taskType := "plan"
	taskPods := []models.TaskPod{}
	if result := h.DB.Where("tfo_resource_uuid = ? AND generation = ? AND task_type = ?", &uuid, &generation, &taskType).Find(&taskPods); result.Error != nil {
		// TODO No plans have been executed yet. This is not an error but we are not able to continue until the plan pod shows up.
		c.JSON(http.StatusOK, response(http.StatusOK, "", responseData))
		return
	}
	taskPod, _ := highestRerun(taskPods, taskType, 0)

	// status := -1
	approvals := []models.Approval{}
	if result := h.DB.Where("task_pod_uuid = ?", &taskPod.UUID).First(&approvals); result.Error != nil {
		c.JSON(http.StatusOK, response(http.StatusOK, "Approval "+result.Error.Error(), []approvalResponse{
			{
				Status: "nodata",
				Approval: models.Approval{
					TaskPodUUID: taskPod.UUID,
				},
			},
		}))
		return
	}

	c.JSON(http.StatusOK, response(http.StatusOK, "", []approvalResponse{
		{
			Approval: approvals[0],
			Status:   "complete",
		},
	}))
}

// UpdateApproval takes the uuid and a JSON data param and create a row in the approval table.
func (h APIHandler) UpdateApproval(c *gin.Context) {
	uuid := c.Param("task_pod_uuid")

	type Approval struct {
		IsApproved bool `json:"is_approved"`
	}
	approvalData := new(Approval)
	err := c.BindJSON(approvalData)
	if err != nil {
		// TODO send message that data is missing or whatever bindjson error returns
		c.JSON(http.StatusNotAcceptable, nil)
		return
	}

	log.Print(approvalData)

	approval := models.Approval{}
	if result := h.DB.Where("task_pod_uuid = ?", &uuid).First(&approval); result.Error == nil {
		// TODO Approval is already set, user should be informed
		c.JSON(http.StatusNoContent, nil)
		return
	} else {
		if !errors.Is(result.Error, gorm.ErrRecordNotFound) {
			// TODO Something unexpected happened
			c.JSON(http.StatusBadRequest, nil)
			return
		}
	}

	approval = models.Approval{
		IsApproved:  approvalData.IsApproved,
		TaskPodUUID: uuid,
	}

	createResult := h.DB.Create(&approval)
	if createResult.Error != nil {
		// TODO Something unexpected happened
		c.JSON(http.StatusBadRequest, nil)
		return
	}

	c.JSON(http.StatusNoContent, nil)

}

type SocketListener struct {
	Connection  *websocket.Conn
	IsListening bool
	EventType   chan int
	Message     []byte
	Err         error
}

// Listen runs a background function and returns a response on the EventType channel.
func (s *SocketListener) Listen() {
	if s.IsListening {
		return
	}
	s.IsListening = true
	s.EventType = make(chan int)
	go func() {
		t, msg, err := s.Connection.ReadMessage()
		s.Message = []byte(msg)
		s.Err = err
		s.IsListening = false
		s.EventType <- t
	}()
}

func (h APIHandler) ResourceLogWatcher(c *gin.Context) {
	tfoResourceUUID := c.Param("tfo_resource_uuid")
	_ = tfoResourceUUID

	var wsupgrader = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
	}
	// how can i make this more secure?
	wsupgrader.CheckOrigin = func(r *http.Request) bool { return true }

	conn, err := wsupgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("Failed to set websocket upgrade: %+v", err)
		return
	}
	defer conn.Close()

	s := SocketListener{Connection: conn}
	s.Listen()

	taskLog := models.TFOTaskLog{}
	if result := h.DB.Last(&taskLog); result.Error != nil {
		return
	}
	lastTaskLogID := taskLog.ID

	logs, err := h.ResourceLogs("", "", "", tfoResourceUUID)
	if err != nil {
		return
	}
	b, err := json.Marshal(logs)
	if err != nil {
		// Why did it fail?
		return
	}
	conn.WriteMessage(1, b)
	for {
		select {
		case i := <-s.EventType:
			if i == -1 {
				log.Println("Closing socket: client is going away")
				return
			}
			if s.Err != nil {
				log.Printf("An error was sent in the socket event: %s", s.Err.Error())
			}
			log.Printf("The event sent was: %s. Listening for next event...", string(s.Message))
			s.Listen()

		case <-time.Tick(1 * time.Second):
			taskLog := models.TFOTaskLog{}
			if result := h.DB.Last(&taskLog); result.Error != nil {
				return
			}
			if lastTaskLogID == taskLog.ID {
				// NOISE --> log.Println("No new logs. Last log id is ", lastTaskLogID)
				continue
			}

			logs, err := h.ResourceLogs("", "", "", tfoResourceUUID)
			if err != nil {
				// Why did it fail?
				return
			}
			b, err := json.Marshal(logs)
			if err != nil {
				// Why did it fail?
				return
			}
			conn.WriteMessage(1, b)
			lastTaskLogID = taskLog.ID
		}
	}

}

// Check if terraform namespace/name resource exists in vcluster
func getResource(parentClientset kubernetes.Interface, clusterName, namespace, name string, ctx context.Context) (*tfv1beta1.Terraform, error) {
	config, err := getVclusterConfig(parentClientset, "internal", clusterName)
	if err != nil {
		return nil, err
	}
	tfoclientset := tfo.NewForConfigOrDie(config)
	return tfoclientset.TfV1beta1().Terraforms(namespace).Get(ctx, name, metav1.GetOptions{})

}

func (h APIHandler) Debugger(c *gin.Context) {
	clusterName := c.Param("cluster_name")
	clusterID := h.getClusterID(clusterName)
	if clusterID == 0 {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, fmt.Sprintf("cluster_name '%s' not found", clusterName), nil))
		return
	}
	name := c.Param("name")
	namespace := c.Param("namespace")
	if _, err := getResource(h.clientset, clusterName, namespace, name, c); err != nil {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, fmt.Sprintf("tf resource '%s/%s' not found", namespace, name), nil))
		return
	}

	cmd := []string{}
	for key, values := range c.Request.URL.Query() {
		if key == "command" {
			cmd = values
		}
	}

	var wsupgrader = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
	}
	// how can i make this more secure?
	wsupgrader.CheckOrigin = func(r *http.Request) bool { return true }

	conn, err := wsupgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("Failed to set websocket upgrade: %+v", err)
		return
	}
	defer conn.Close()

	podExecReadWriter, err := New(h.clientset, clusterName, namespace, name, c, cmd)
	if err != nil {
		log.Printf("Failed to connect to debug pod: %s", err)
		return
	}

	var opts []webtty.Option
	opts = append(opts, webtty.WithPermitWrite())

	webTTY, err := webtty.New(&wsWrapper{conn}, podExecReadWriter, opts...)
	if err != nil {
		log.Printf("failed to create webtty: %s", err)
		return
	}

	errCh := make(chan error)
	go func() {
		errCh <- webTTY.Run(c)
	}()

	select {
	case err = <-errCh:
		log.Println(err)
		msg := websocket.FormatCloseMessage(websocket.CloseGoingAway, "")
		err := conn.WriteMessage(websocket.CloseMessage, msg)
		if err != nil {
			log.Println(err)
		}
	case err = <-podExecReadWriter.Closer:
		log.Println("Closing the connection")
		var closeCode int
		var msg string
		if err != nil {
			closeCode = websocket.CloseGoingAway
			msg = err.Error()
		} else {
			closeCode = websocket.CloseNormalClosure
			msg = ""
		}
		closeMsg := websocket.FormatCloseMessage(closeCode, msg)
		err := conn.WriteMessage(websocket.CloseMessage, closeMsg)
		if err != nil {
			log.Println(err)
		}
	}

	// return
}

type wsWrapper struct {
	*websocket.Conn
}

func (wsw *wsWrapper) Write(p []byte) (n int, err error) {
	// log.Println("wsWrapper.Write ", string(p))
	writer, err := wsw.Conn.NextWriter(websocket.TextMessage)
	if err != nil {
		return 0, err
	}
	defer writer.Close()
	return writer.Write(p)
}

func (wsw *wsWrapper) Read(p []byte) (n int, err error) {
	// log.Println("wsWrapper.Read ", string(p))
	for {
		msgType, reader, err := wsw.Conn.NextReader()
		if err != nil {
			// log.Println("I didn't read it cuz err", err)
			return 0, err
		}

		if msgType != websocket.TextMessage {
			// log.Println("I didn't read it cuz wrong type")
			continue
		}

		b, err := ioutil.ReadAll(reader)
		if len(b) > len(p) {
			// log.Println("I didn't read it cuz another error", err)
			return 0, fmt.Errorf("client message exceeded buffer size: %s", err)
		}
		// log.Println("I did read something into a reader which produced this: %s", string(b))

		// log.Println("I think I read it but I'm not 100%% sure", err)
		dec, err := base64.StdEncoding.DecodeString(string(b[1:]))
		if err != nil {
			// log.Println("I didn't read it cuz could not decode", err)
			continue
		}
		// log.Printf("I'm reading dec = %s ", string(dec))

		n = copy(p, append([]byte{b[0]}, dec...))
		return n, err
		// n, err = wsw.podTTY.Write(dec)
		// if err != nil {
		// 	return 0, err
		// }

	}
}

// type PodExecFactory struct{}
type PodExec struct {
	pty       *os.File
	reader    io.Reader
	writer    io.Writer
	termSizer TermSizer
	Closer    chan error
}

type TermSizer struct {
	SizeCh chan remotecommand.TerminalSize
}

func (t TermSizer) Next() *remotecommand.TerminalSize {
	size := <-t.SizeCh
	return &size
}

// func (f *PodExecFactory) Name() string {
// 	return "Pod Exec"
// }

// func (f *PodExecFactory) New(params map[string][]string, headers map[string][]string) (Slave, error) {
// 	return New()
// }

// command string, argv []string, headers map[string][]string, options ...Option
func New(clientset kubernetes.Interface, clusterName, namespace, name string, c *gin.Context, cmd []string) (*PodExec, error) {
	pty, tty, err := ptylib.Open()
	if err != nil {
		log.Fatal(err)
	}
	os.Stdin = tty
	os.Stderr = tty
	os.Stdout = tty

	sizeCh := make(chan remotecommand.TerminalSize)
	closeCh := make(chan error)

	termSizer := TermSizer{
		SizeCh: sizeCh,
	}

	go func() {
		defer pty.Close()
		err := RemoteDebug(clientset, clusterName, namespace, name, tty, c, termSizer, cmd)
		log.Println("Pod exec exited")
		closeCh <- err
	}()

	return &PodExec{
		pty:       pty,
		reader:    pty,
		writer:    pty,
		termSizer: termSizer,
		Closer:    closeCh,
	}, nil

}

func (podExec *PodExec) Read(p []byte) (n int, err error) {
	return podExec.reader.Read(p)
}

func (podExec *PodExec) Write(p []byte) (n int, err error) {
	return podExec.writer.Write(p)
}

// WindowTitleVariables returns any values that can be used to fill out
// the title of a terminal.
func (p *PodExec) WindowTitleVariables() map[string]interface{} {
	return map[string]interface{}{
		"command":  "tfo",
		"hostname": "localhost",
	}
}

// ResizeTerminal sets a new size of the terminal.
func (p *PodExec) ResizeTerminal(columns int, rows int) error {
	// log.Println("Resizing Terminal", columns, rows)

	p.termSizer.SizeCh <- remotecommand.TerminalSize{
		Width:  uint16(columns),
		Height: uint16(rows),
	}

	return nil
}

func (p *PodExec) Close() error {
	log.Println("Going away...")
	return nil
}

// RemoteDebug starts the debug pod and connects in a tty that will be synced thru a websocket. Anything written to
// stdout will be synced to the tty. stderr logs will show up in the api logs and not the tty.
func RemoteDebug(parentClientset kubernetes.Interface, clusterName, namespace, name string, tty *os.File, c *gin.Context, terminalSizeQueue remotecommand.TerminalSizeQueue, cmd []string) error {

	config, err := getVclusterConfig(parentClientset, "internal", clusterName)
	if err != nil {
		return err
	}
	tfoclientset := tfo.NewForConfigOrDie(config)
	clientset := kubernetes.NewForConfigOrDie(config)
	tfclient := tfoclientset.TfV1beta1().Terraforms(namespace)
	// newSession()

	// tfoclientset := session.tfoclientset.TfV1beta1().Terraforms(namespace)
	podClient := clientset.CoreV1().Pods(namespace)

	tf, err := tfclient.Get(c, name, metav1.GetOptions{})
	if err != nil {
		return err
	}

	pod := generatePod(tf)
	pod, err = podClient.Create(c, pod, metav1.CreateOptions{})
	if err != nil {
		return err
	}
	defer podClient.Delete(c, pod.Name, metav1.DeleteOptions{})

	fmt.Printf("Connecting to %s ", pod.Name)

	watcher, err := podClient.Watch(c, metav1.ListOptions{
		FieldSelector: "metadata.name=" + pod.Name,
	})
	if err != nil {
		return err
	}

	for event := range watcher.ResultChan() {
		fmt.Printf(".")
		switch event.Type {
		case watch.Modified:
			pod = event.Object.(*corev1.Pod)
			// If the Pod contains a status condition Ready == True, stop
			// watching.
			for _, cond := range pod.Status.Conditions {
				if cond.Type == corev1.PodReady &&
					cond.Status == corev1.ConditionTrue &&
					pod.Status.Phase == corev1.PodRunning {
					watcher.Stop()
				}
			}
		default:
			// fmt.Fprintln(os.Stderr, event.Type)
		}
	}

	// log.Printf("tty isTerminal? %t", isTerminal(tty))
	ioStreams := genericclioptions.IOStreams{In: tty, Out: tty, ErrOut: tty}
	// log.Println("Setting up steamOptions")
	streamOptions := exec.StreamOptions{
		IOStreams: ioStreams,
		Stdin:     true,
		TTY:       true,
	}
	// log.Println("Checking TTY setup")
	t := streamOptions.SetupTTY()
	if t.Raw {
		// unset p.Err if it was previously set because both stdout and stderr go over p.Out when tty is
		// true
		streamOptions.ErrOut = nil
	}
	// log.Println(file.Name())
	// log.Println("Setting up request")
	execCommand := []string{
		"/bin/bash",
		"-c",
		`cd $TFO_MAIN_MODULE && \
			export PS1="\\w\\$ " && \
			if [[ -n "$AWS_WEB_IDENTITY_TOKEN_FILE" ]]; then
				export $(irsa-tokengen);
				echo printf "\nAWS creds set from token file\n"
			fi && \
			printf "\nTry running 'terraform init'\n\n" && bash
		`,
	}

	if len(cmd) > 0 {
		execCommand = cmd
	}

	fn := func() error {
		req := clientset.CoreV1().RESTClient().
			Post().
			Namespace(pod.Namespace).
			Resource("pods").
			Name(pod.Name).
			SubResource("exec").
			VersionedParams(&corev1.PodExecOptions{
				Container: pod.Spec.Containers[0].Name,
				Command:   execCommand,
				// Stdin:  streamOptions.Stdin,
				// Stdout: streamOptions.Out != nil,
				// Stderr: streamOptions.ErrOut != nil,
				Stdin:  true,
				Stdout: true,
				Stderr: true,
				TTY:    t.Raw,
			}, scheme.ParameterCodec)

		return func() error {

			exec, err := remotecommand.NewSPDYExecutor(config, "POST", req.URL())
			if err != nil {
				return err
			}

			return exec.StreamWithContext(c, remotecommand.StreamOptions{
				Stdin:             streamOptions.In,
				Stdout:            streamOptions.Out,
				Stderr:            streamOptions.ErrOut,
				Tty:               t.Raw,
				TerminalSizeQueue: terminalSizeQueue,
			})
		}()

	}

	if err := t.Safe(fn); err != nil {
		return err
	}
	return nil
}

// func isTerminal(file *os.File) bool {

// 	inFd := file.Fd()
// 	log.Println(file.Name(), inFd)
// 	_, err := unix.IoctlGetTermios(int(inFd), unix.TIOCGETA)
// 	return err == nil

// }

func generatePod(tf *tfv1beta1.Terraform) *corev1.Pod {
	terraformVersion := tf.Spec.TerraformVersion
	if terraformVersion == "" {
		terraformVersion = "1.1.5"
	}
	generation := fmt.Sprint(tf.Generation)
	versionedName := tf.Status.PodNamePrefix + "-v" + generation
	generateName := versionedName + "-debug-"
	generationPath := "/home/tfo-runner/generations/" + generation
	env := []corev1.EnvVar{}
	envFrom := []corev1.EnvFromSource{}
	annotations := make(map[string]string)
	labels := make(map[string]string)
	for _, taskOption := range tf.Spec.TaskOptions {
		if tfv1beta1.ListContainsTask(taskOption.For, "*") {
			env = append(env, taskOption.Env...)
			envFrom = append(envFrom, taskOption.EnvFrom...)
			for key, value := range taskOption.Annotations {
				annotations[key] = value
			}
			for key, value := range taskOption.Labels {
				labels[key] = value
			}
		}
	}
	env = append(env, []corev1.EnvVar{
		{
			Name:  "TFO_TASK",
			Value: "debug",
		},
		{
			Name:  "TFO_RESOURCE",
			Value: tf.Name,
		},
		{
			Name:  "TFO_NAMESPACE",
			Value: tf.Namespace,
		},
		{
			Name:  "TFO_GENERATION",
			Value: generation,
		},
		{
			Name:  "TFO_GENERATION_PATH",
			Value: generationPath,
		},
		{
			Name:  "TFO_MAIN_MODULE",
			Value: generationPath + "/main",
		},
		{
			Name:  "TFO_TERRAFORM_VERSION",
			Value: tf.Spec.TerraformVersion,
		},
	}...)

	volumes := []corev1.Volume{
		{
			Name: "tfohome",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: tf.Status.PodNamePrefix,
					ReadOnly:  false,
				},
			},
		},
	}
	volumeMounts := []corev1.VolumeMount{
		{
			Name:      "tfohome",
			MountPath: "/home/tfo-runner",
			ReadOnly:  false,
		},
	}
	env = append(env, corev1.EnvVar{
		Name:  "TFO_ROOT_PATH",
		Value: "/home/tfo-runner",
	})

	optional := true
	xmode := int32(0775)
	volumes = append(volumes, corev1.Volume{
		Name: "gitaskpass",
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				SecretName: versionedName,
				Optional:   &optional,
				Items: []corev1.KeyToPath{
					{
						Key:  "gitAskpass",
						Path: "GIT_ASKPASS",
						Mode: &xmode,
					},
				},
			},
		},
	})
	volumeMounts = append(volumeMounts, []corev1.VolumeMount{
		{
			Name:      "gitaskpass",
			MountPath: "/git/askpass",
		},
	}...)
	env = append(env, []corev1.EnvVar{
		{
			Name:  "GIT_ASKPASS",
			Value: "/git/askpass/GIT_ASKPASS",
		},
	}...)

	for _, c := range tf.Spec.Credentials {
		if c.AWSCredentials.KIAM != "" {
			annotations["iam.amazonaws.com/role"] = c.AWSCredentials.KIAM
		}
	}

	for _, c := range tf.Spec.Credentials {
		if (tfv1beta1.SecretNameRef{}) != c.SecretNameRef {
			envFrom = append(envFrom, []corev1.EnvFromSource{
				{
					SecretRef: &corev1.SecretEnvSource{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: c.SecretNameRef.Name,
						},
					},
				},
			}...)
		}
	}

	labels["terraforms.tf.galleybytes.com/generation"] = generation
	labels["terraforms.tf.galleybytes.com/resourceName"] = tf.Name
	labels["terraforms.tf.galleybytes.com/podPrefix"] = tf.Status.PodNamePrefix
	labels["terraforms.tf.galleybytes.com/terraformVersion"] = tf.Spec.TerraformVersion
	labels["app.kubernetes.io/name"] = "terraform-operator"
	labels["app.kubernetes.io/component"] = "terraform-operator-cli"
	labels["app.kubernetes.io/instance"] = "debug"
	labels["app.kubernetes.io/created-by"] = "cli"

	initContainers := []corev1.Container{}
	containers := []corev1.Container{}

	// Make sure to use the same uid for containers so the dir in the
	// PersistentVolume have the correct permissions for the user
	user := int64(0)
	group := int64(2000)
	runAsNonRoot := false
	privileged := true
	allowPrivilegeEscalation := true
	seLinuxOptions := corev1.SELinuxOptions{}
	securityContext := &corev1.SecurityContext{
		RunAsUser:                &user,
		RunAsGroup:               &group,
		RunAsNonRoot:             &runAsNonRoot,
		Privileged:               &privileged,
		AllowPrivilegeEscalation: &allowPrivilegeEscalation,
		SELinuxOptions:           &seLinuxOptions,
	}
	restartPolicy := corev1.RestartPolicyNever

	containers = append(containers, corev1.Container{
		SecurityContext: securityContext,
		Name:            "debug",
		Image:           "ghcr.io/galleybytes/terraform-operator-tftaskv1.1.0:" + terraformVersion,
		Command: []string{
			"/bin/sleep", "86400",
		},
		ImagePullPolicy: corev1.PullIfNotPresent,
		EnvFrom:         envFrom,
		Env:             env,
		VolumeMounts:    volumeMounts,
	})

	podSecurityContext := corev1.PodSecurityContext{
		FSGroup: &group,
	}
	serviceAccount := tf.Spec.ServiceAccount
	if serviceAccount == "" {
		// By prefixing the service account with "tf-", IRSA roles can use wildcard
		// "tf-*" service account for AWS credentials.
		serviceAccount = "tf-" + versionedName
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: generateName,
			Namespace:    tf.Namespace,
			Labels:       labels,
			Annotations:  annotations,
		},
		Spec: corev1.PodSpec{
			SecurityContext:    &podSecurityContext,
			ServiceAccountName: serviceAccount,
			RestartPolicy:      restartPolicy,
			InitContainers:     initContainers,
			Containers:         containers,
			Volumes:            volumes,
		},
	}

	return pod
}
