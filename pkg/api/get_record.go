package api

import (
	"encoding/json"
	"errors"
	"log"
	"math"
	"net/http"
	"time"

	"github.com/GalleyBytes/terraform-operator-api/pkg/common/models"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"gopkg.in/yaml.v3"
	"gorm.io/gorm"
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

func (h handler) GetDistinctGeneration(c *gin.Context) {
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

func (h handler) GetUuidByClusterID(c *gin.Context) {
	clusterID := c.Param("cluster_id")
	var clusterIdInfo models.TFOResource

	if result := h.DB.Where("cluster_id = ?", clusterID).First(&clusterIdInfo); result.Error != nil {
		c.AbortWithError(http.StatusNotFound, result.Error)
		return
	}

	c.JSON(http.StatusOK, &clusterIdInfo)

}

func (h handler) GeIdByClusterName(c *gin.Context) {
	clusterName := c.Param("cluster_name")
	var clusterNameInfo models.Cluster

	if result := h.DB.Where("name = ?", clusterName).First(&clusterNameInfo); result.Error != nil {
		// TODO should not return a error status code just because the database is missing this data. Must
		// do better error handling here. (This applies in lots of other places in the code, I just wanted
		// to officially make a todo item to clean up the api)
		c.AbortWithError(http.StatusNotFound, result.Error)
		return
	}

	c.JSON(http.StatusOK, &clusterNameInfo)

}

func (h handler) Index(c *gin.Context) {
	// TODO return api discovery data
	c.JSON(http.StatusNoContent, nil)
}

func (h handler) GetClusters(c *gin.Context) {
	var clusters []models.Cluster

	if result := h.DB.Find(&clusters); result.Error != nil {
		c.AbortWithError(http.StatusNotFound, result.Error)
		return
	}

	c.JSON(http.StatusOK, &clusters)
}

func (h handler) GetClustersResources(c *gin.Context) {
	var resources []models.TFOResource
	clusterID := c.Param("cluster_id")

	if result := h.DB.Where("cluster_id = ?", clusterID).Find(&resources); result.Error != nil {
		c.AbortWithError(http.StatusNotFound, result.Error)
		return
	}

	c.JSON(http.StatusOK, &resources)
}

func (h handler) GetResourceByUUID(c *gin.Context) {
	var tfoResource models.TFOResource
	uuid := c.Param("tfo_resource_uuid")

	if result := h.DB.First(&tfoResource, "uuid = ?", uuid); result.Error != nil {
		c.AbortWithError(http.StatusNotFound, result.Error)
		return
	}

	c.JSON(http.StatusOK, &tfoResource)
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

func (h handler) LatestGeneration(uuid string) string {
	var tfoResource models.TFOResource
	if result := h.DB.First(&tfoResource, "uuid = ?", &uuid); result.Error != nil {
		return ""
	}
	return tfoResource.CurrentGeneration
}

// ResourceLogs data contract for clients to consume
type ResourceLogs struct {
	ID              uint   `json:"id"`
	LogMessage      string `json:"message"`
	TaskType        string `json:"task_type"`
	Rerun           int    `json:"rerun"`
	LineNo          string `json:"line_no"`
	TFOResourceUUID string `json:"tfo_resource_uuid"`
}

// GetClustersResourceLogs will return the latest logs for the selected resource. The only filted allowed
// in this call is the generation to switch getting the latest logs for a given generation.
func (h handler) GetClustersResourcesLogs(c *gin.Context) {

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

func (h handler) ResourceLogs(generationFilter, rerunFilter, taskTypeFilter, uuid string) ([]ResourceLogs, error) {
	logs := []ResourceLogs{}
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

	// Log order:
	taskTypesInOrder := []string{
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
	if result := h.DB.Where("tfo_resource_uuid = ? AND task_pod_uuid IN ?", &uuid, taskPodUUIDs).Find(&tfoTaskLogs); result.Error != nil {
		return logs, result.Error
	}

	// TODO optimize the taskPod/taskLog matching algorithm, leaving this simple lookup since logs are
	// unlikely to be more than just a few thousand lines max. This number should be easily handled.
	for _, taskPod := range taskPodsOfHighestRerun {
		for _, log := range tfoTaskLogs {
			if log.TaskPodUUID == taskPod.UUID {
				logs = append(logs, ResourceLogs{
					ID:              log.ID,
					LogMessage:      log.Message,
					LineNo:          log.LineNo,
					Rerun:           taskPod.Rerun,
					TaskType:        taskPod.TaskType,
					TFOResourceUUID: log.TFOResourceUUID,
				})
			}
		}
	}
	return logs, nil
}

func (h handler) LookupResourceSpec(generation, uuid string) *models.TFOResourceSpec {
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

func (h handler) GetResourceSpec(c *gin.Context) {
	uuid := c.Param("tfo_resource_uuid")
	generation := c.Param("generation")
	tfoResourceSpec := h.LookupResourceSpec(generation, uuid)

	responseData := []interface{}{}
	if tfoResourceSpec != nil {
		responseData = append(responseData, GetResourceSpecResponseData{TFOResourceSpec: *tfoResourceSpec})
	}
	c.JSON(http.StatusOK, &responseData)
}

type GetApprovalStatusResponseData struct {
	TFOResourceUUID string `json:"tfo_resource_uuid"`
	TaskPodUUID     string `json:"task_pod_uuid"`

	// Status is fuzzy. -1 means it hasn't been decided, 0 is false, 1 is true for the approvals.
	// Hasn't been decided means there is no record in the approvals table matching the uuid.
	Status int `json:"status"`
}

// GetApprovalStatus only looks at the latest resource spec by getting the TFOResource's 'LatestGeneration'.
// Use the generation to get the TFOResourceSpec and parses the "spec" for the requireApproval value. If the
// value is "true", this function finds the latest plan task by getting the TaskPod with the highest rerun number.
// The UUID of the TaskPod is used to lookup the Approval status to return to the caller.
func (h handler) GetApprovalStatus(c *gin.Context) {
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
		c.JSON(http.StatusOK, responseData)
		return
	}

	spec := struct {
		RequireApproval bool `yaml:"requireApproval"`
	}{}

	err := yaml.Unmarshal([]byte(tfoResourceSpec.ResourceSpec), &spec)
	if err != nil {
		// TODO What's the error messsage?
		c.JSON(http.StatusOK, responseData)
		return
	}

	if !spec.RequireApproval {
		// TODO what's the message when no require approval is required?
		c.JSON(http.StatusOK, responseData)
		return
	}

	taskType := "plan"
	taskPods := []models.TaskPod{}
	if result := h.DB.Where("tfo_resource_uuid = ? AND generation = ? AND task_type = ?", &uuid, &generation, &taskType).Find(&taskPods); result.Error != nil {
		// TODO No plans have been executed yet. This is not an error but we are not able to continue until the plan pod shows up.
		c.JSON(http.StatusOK, responseData)
		return
	}
	taskPod, _ := highestRerun(taskPods, taskType, 0)

	status := -1
	approval := models.Approval{}
	if result := h.DB.Where("task_pod_uuid = ?", &taskPod.UUID).First(&approval); result.Error != nil {
		// TODO handle error not related to does not exist
		c.JSON(http.StatusOK, GetApprovalStatusResponseData{
			TFOResourceUUID: uuid,
			TaskPodUUID:     taskPod.UUID,
			Status:          status,
		})
		return
	}
	// TODO What is the approval status response structure going to look like?
	if approval.IsApproved {
		status = 1
	} else {
		status = 0
	}
	c.JSON(http.StatusOK, GetApprovalStatusResponseData{
		TFOResourceUUID: uuid,
		TaskPodUUID:     taskPod.UUID,
		Status:          status,
	})

}

// UpdateApproval takes the uuid and a JSON data param and create a row in the approval table.
func (h handler) UpdateApproval(c *gin.Context) {
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

func (h handler) ResourceLogWatcher(c *gin.Context) {
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
