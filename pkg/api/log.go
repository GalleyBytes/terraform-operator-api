package api

import (
	"fmt"
	"log"
	"net/http"
	"strconv"

	"github.com/galleybytes/terraform-operator-api/pkg/common/models"
	"github.com/gin-gonic/gin"
)

func (h APIHandler) AddTaskPod(c *gin.Context) {

	jsonData := struct {
		TFOResourceUUID string `json:"tfo_resource_uuid"`
		Generation      string `json:"tfo_resource_generation"`
		RerunID         string `json:"rerun_id"`
		TaskName        string `json:"task_name"`
		UUID            string `json:"uuid"`
		Content         string `json:"content"`
	}{}
	err := c.BindJSON(&jsonData)
	if err != nil {
		log.Println(err)
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, err.Error(), nil))
		return
	}
	if jsonData.UUID == "" {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, "missing request data", nil))
		return
	}

	rerunID, err := strconv.Atoi(jsonData.RerunID)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, fmt.Sprintf("rerun is must be an int, got %s", jsonData.RerunID), nil))
		return
	}

	taskPod := models.TaskPod{
		UUID:            jsonData.UUID,
		TaskType:        jsonData.TaskName,
		Generation:      jsonData.Generation,
		Rerun:           rerunID,
		TFOResourceUUID: jsonData.TFOResourceUUID,
	}
	result := h.DB.Where("uuid = ?", &jsonData.UUID).FirstOrCreate(&taskPod)
	if result.Error != nil {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, result.Error.Error(), nil))
		return
	}

	if jsonData.Content == "" {
		c.JSON(http.StatusOK, response(http.StatusOK, "", []models.TaskPod{taskPod}))
	}

	// Combine the creation or finding of the taskPod with logging when content is sent
	err = h.saveTaskLog(taskPod.UUID, jsonData.Content)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, err.Error(), nil))
		return
	}

	c.JSON(http.StatusNoContent, nil)

}

func (h APIHandler) saveTaskLog(taskUUID, content string) error {

	taskLog := models.TFOTaskLog{
		TaskPodUUID: taskUUID,
		Message:     content,
		Size:        uint64(len([]byte(content))),
	}

	if result := h.DB.Where("task_pod_uuid = ?", &taskLog.TaskPodUUID).FirstOrCreate(&taskLog); result.Error != nil {
		return fmt.Errorf("failed to save task log: %+v, %+v", taskLog, result.Error)
	}

	if taskLog.Size != uint64(len([]byte(content))) {
		if taskLog.Size > uint64(len([]byte(content))) {
			return fmt.Errorf("The message size was smaller than already recorded")
		}
		// The content has been updated. Read the bytes after what has already been written to preserve the
		// original content. We don't want to allow logs in the database to be changed once they are written.
		taskLog.Message += string([]byte(content)[taskLog.Size:])
		taskLog.Size = uint64(len([]byte(taskLog.Message)))
		if result := h.DB.Save(&taskLog); result.Error != nil {
			return result.Error
		}
	}

	return nil
}

func (h APIHandler) AddTFOTaskLogs(c *gin.Context) {
	jsonData := struct {
		TFOTaskLogs []models.TFOTaskLog `json:"tfo_task_logs"`
	}{}
	err := c.BindJSON(&jsonData)
	if err != nil {
		log.Println(err)
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, err.Error(), nil))
		return
	}
	result := h.DB.Create(&jsonData.TFOTaskLogs)
	if result.Error != nil {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, result.Error.Error(), nil))
		return
	}

	c.JSON(http.StatusNoContent, nil)
}
