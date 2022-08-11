package api

import (
	"fmt"
	"math"
	"net/http"

	"github.com/GalleyBytes/terraform-operator-api/pkg/common/models"
	"github.com/gin-gonic/gin"
)

type Response struct {
	StatusInfo StatusInfo  `json:"StatusInfo"`
	Data       interface{} `json:"Data"`
}

type StatusInfo struct {
	StatusCode int64  `json:"StatusCode"`
	Message    string `json:"Message"`
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

func (h handler) GetLog(c *gin.Context) {
	uuid := c.Param("tfo_resource_uuid")
	var log models.TFOTaskLog

	if result := h.DB.First(&log, "tfo_resource_uuid = ?", uuid); result.Error != nil {
		c.AbortWithError(http.StatusNotFound, result.Error)
		resp := response(http.StatusNotFound, "tfo resource uuid not found", log)
		c.JSON(int(resp.StatusInfo.StatusCode), &resp)
		return
	}
	resp := response(http.StatusOK, "tfo resource uuid found", log)
	c.JSON(int(resp.StatusInfo.StatusCode), &resp)
}

func (h handler) GetLogByGeneration(c *gin.Context) {
	uuid := c.Param("tfo_resource_uuid")
	generation := c.Param("generation")
	var generationLogs []models.TFOTaskLog

	if result := h.DB.Where("generation = ? AND tfo_resource_uuid = ?", &generation, &uuid).Find(&generationLogs); result.Error != nil {
		c.AbortWithError(http.StatusNotFound, result.Error)
		resp := response(http.StatusNotFound, "generation logs not found", generationLogs)
		c.JSON(int(resp.StatusInfo.StatusCode), &resp)
		return
	}

	resp := response(http.StatusOK, "generation logs found", generationLogs)
	c.JSON(int(resp.StatusInfo.StatusCode), &resp)

}

func (h handler) GetDistinctGeneration(c *gin.Context) {
	uuid := c.Param("tfo_resource_uuid")
	var generation []int
	if result := h.DB.Raw("SELECT DISTINCT generation FROM tfo_task_logs WHERE tfo_resource_uuid = ?", &uuid).Scan(&generation); result.Error != nil {
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
		c.AbortWithError(http.StatusNotFound, result.Error)
		return
	}

	c.JSON(http.StatusOK, &clusterNameInfo)

}

func (h handler) GetRecords(c *gin.Context) {
	var records []models.TFOResource

	if result := h.DB.Find(&records); result.Error != nil {
		c.AbortWithError(http.StatusNotFound, result.Error)
		return
	}

	c.JSON(http.StatusOK, &records)
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

// func (h handler) GetResourceLogsByUUID(c *gin.Context) {
// 	var tfoResource models.TFOTaskLog
// 	uuid := c.Param("tfo_resource_uuid")

// 	if result := h.DB.First(&tfoResource, "uuid = ?", uuid); result.Error != nil {
// 		c.AbortWithError(http.StatusNotFound, result.Error)
// 		return
// 	}

// 	c.JSON(http.StatusOK, &tfoResource)
// }

func highestRerun(taskLogs []models.TFOTaskLog, taskType string, minimum float64) ([]models.TFOTaskLog, float64) {
	logs := []models.TFOTaskLog{}
	highestRerunObservedInLogs := 0
	for _, taskLog := range taskLogs {
		if taskLog.TaskType == taskType {
			if taskLog.Rerun > highestRerunObservedInLogs {
				highestRerunObservedInLogs = taskLog.Rerun
			}
		}
	}

	// return only the highest rerun. It is ok to return an empty list, this just indicates that the task
	// has not produced logs yet.
	rerun := math.Max(float64(highestRerunObservedInLogs), minimum)
	for _, taskLog := range taskLogs {
		if taskLog.TaskType == taskType && taskLog.Rerun == int(rerun) {
			logs = append(logs, taskLog)
		}
	}
	return logs, rerun
}

func (h handler) GetClustersResourcesLogs(c *gin.Context) {
	var logs []models.TFOTaskLog
	var tfoResource models.TFOResource
	//clusterID := c.Param("cluster")
	generation := c.Param("generation")
	uuid := c.Param("tfo_resource_uuid")

	// if result := h.DB.Where("cluster_id = ?", clusterID).Find(&tfoResource); result.Error != nil {
	// 	c.AbortWithError(http.StatusNotFound, result.Error)
	// 	return
	// }
	// uuid := tfoResource.UUID

	if generation == "latest" {
		if result := h.DB.First(&tfoResource, "uuid = ?", &uuid); result.Error != nil {
			c.AbortWithError(http.StatusNotFound, result.Error)
			return
		}
		generation = tfoResource.CurrentGeneration
	}

	if result := h.DB.Where("tfo_resource_uuid = ? AND generation = ?", &uuid, &generation).Find(&logs); result.Error != nil {
		c.AbortWithError(http.StatusNotFound, result.Error)
		return
	}

	// We've retrieved all the logs for the generation at this point, but we need to ensure we only display
	// the latest rerun logs. If a user wants a specific rerun, a different query will need to be constructed

	// We must have knowledge of the order of logs to return the correct result to the user. Each task type
	// can have a different rerun number, and therefore, we have to make sure that when the results are
	// returned, the latest tasks do not have earlier reruns than previous tasks.
	//
	//    9 |                       |
	//    8 |                       |
	// N  7 |   Good Results        |   Bad result becuase the apply log
	// U  6 |                       |   doesn't reflect the result of plan
	// R  5 |                       |
	// E  4 |                       |             x
	// R  3 |         x   x         |                x
	//    2 |                       |
	//    1 |    x                  |        x
	//    0 |_x_____________________|____x________________________
	//       p   t   n   y               p   t   n   y
	//      u   i   a   l               u   i   a   l
	//     t   n   l   p               t   n   l   p
	//    e   i   p   p               e   i   p   p
	//   s           a               s           a

	// Create this in order to store the logs filtered by the rerun sort
	filteredResuilts := []models.TFOTaskLog{}

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

	currentRerun := float64(0)
	for _, taskLog := range taskTypesInOrder {
		highestRerunLogs, rerun := highestRerun(logs, taskLog, currentRerun)
		filteredResuilts = append(filteredResuilts, highestRerunLogs...)
		currentRerun = rerun
	}

	c.JSON(http.StatusOK, &filteredResuilts)
}

func (h handler) GetRerunByNumber(c *gin.Context) {
	uuid := c.Param("tfo_resource_uuid")
	taskType := c.Param("task_type")
	generation := c.Param("generation")
	var rerunNumbers []models.TFOTaskLog
	rerunValue := c.Param("rerun_value")
	var tfoResource models.TFOResource
	//todo query for latest

	if generation == "latest" {
		if result := h.DB.First(&tfoResource, "uuid = ?", &uuid); result.Error != nil {
			c.AbortWithError(http.StatusNotFound, result.Error)
			return
		}
		generation = tfoResource.CurrentGeneration
	}

	if result := h.DB.Where("task_type = ? AND tfo_resource_uuid = ? AND rerun = ? AND generation = ?", &taskType, &uuid, &rerunValue, &generation).Find(&rerunNumbers); result.Error != nil {
		c.AbortWithError(http.StatusNotFound, result.Error)
		return
	}

	c.JSON(http.StatusOK, &rerunNumbers)
}

func (h handler) GetHighestRerunLog(c *gin.Context) {
	generation := c.Param("generation")
	var maxRerun int

	if result := h.DB.Raw("SELECT MAX(rerun) FROM tfo_task_logs WHERE generation = ?", &generation).Scan(&maxRerun); result.Error != nil {
		c.AbortWithError(http.StatusNotFound, result.Error)
		return
	}
	c.JSON(http.StatusOK, &maxRerun)
}

func (h handler) GetHighestRerunLogForTFO(c *gin.Context) {
	uuid := c.Param("tfo_resource_uuid")
	var init models.TFOTaskLog
	var setup models.TFOTaskLog

	if result := h.DB.Raw("SELECT * FROM tfo_task_logs WHERE tfo_resource_uuid = ? AND task_type = 'init' AND rerun = (select MAX(rerun) from tfo_task_logs)", &uuid).Scan(&init); result.Error != nil {
		//if result := h.DB.Raw("SELECT MAX(rerun) FROM tfo_task_logs WHERE tfo_resource_uuid = ?", &uuid).Scan(&maxRerun); result.Error != nil {
		c.AbortWithError(http.StatusNotFound, result.Error)
		return
	}
	fmt.Println(init)
	c.JSON(http.StatusOK, &init)

	// SELECT * FROM tfo_task_logs WHERE tfo_resource_uuid = '3ee03fd7-d7ac-4fbd-8c4c-d573bc7360ef' AND rerun = (select MAX(rerun) from tfo_task_logs);
	if result := h.DB.Raw("SELECT * FROM tfo_task_logs WHERE tfo_resource_uuid = ? AND task_type = 'setup' AND rerun = (select MAX(rerun) from tfo_task_logs)", &uuid).Scan(&setup); result.Error != nil {
		//if result := h.DB.Raw("SELECT MAX(rerun) FROM tfo_task_logs WHERE tfo_resource_uuid = ?", &uuid).Scan(&maxRerun); result.Error != nil {
		c.AbortWithError(http.StatusNotFound, result.Error)
		return
	}
	fmt.Println(setup)
	c.JSON(http.StatusOK, &setup)
}

func (h handler) GetResourceSpec(c *gin.Context) {
	uuid := c.Param("tfo_resource_uuid")
	generation := c.Param("generation")
	var tfoResource models.TFOResource
	var tfoResourcespec []models.TFOResourceSpec

	if generation == "latest" {
		if result := h.DB.First(&tfoResource, "uuid = ?", &uuid); result.Error != nil {
			c.AbortWithError(http.StatusNotFound, result.Error)
			return
		}
		generation = tfoResource.CurrentGeneration
	}

	if result := h.DB.Where("tfo_resource_uuid = ? AND generation =?", uuid, generation).Find(&tfoResourcespec); result.Error != nil {
		c.AbortWithError(http.StatusNotFound, result.Error)
		return
	}

	c.JSON(http.StatusOK, &tfoResourcespec)
}
