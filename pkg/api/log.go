package api

import (
	"fmt"
	"log"
	"net/http"
	"strconv"

	"github.com/galleybytes/terraform-operator-api/pkg/common/models"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func (h APIHandler) AddTaskPod(c *gin.Context) {
	token, err := taskJWT(c.Request.Header["Token"][0])
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, err.Error(), nil))
		return
	}

	claims := taskJWTClaims(token)
	resourceUUID := claims["resourceUUID"]
	generation := claims["generation"]

	jsonData := struct {
		RerunID             string `json:"rerun_id"`
		TaskName            string `json:"task_name"`
		UUID                string `json:"uuid"`
		InClusterGeneration string `json:"generation"`

		Content string `json:"content"`
	}{}
	err = c.BindJSON(&jsonData)
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
		UUID:                jsonData.UUID,
		TaskType:            jsonData.TaskName,
		Generation:          generation,
		Rerun:               rerunID,
		TFOResourceUUID:     resourceUUID,
		InClusterGeneration: jsonData.InClusterGeneration,
	}
	result := h.DB.Where("uuid = ?", &jsonData.UUID).FirstOrCreate(&taskPod)
	if result.Error != nil {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, result.Error.Error(), nil))
		return
	}

	if jsonData.Content == "" {
		c.JSON(http.StatusOK, response(http.StatusOK, "", []models.TaskPod{taskPod}))
		return
	}

	// Task calls will generally contain content. Save the message to the database.
	err = saveTaskLog(h.DB, taskPod.UUID, jsonData.Content)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, err.Error(), nil))
		return
	}

	c.JSON(http.StatusNoContent, nil)

}

// Write or update logs in database
func saveTaskLog(db *gorm.DB, taskUUID, content string) error {

	taskLog := models.TFOTaskLog{
		TaskPodUUID: taskUUID,
		Message:     content,
		Size:        uint64(len([]byte(content))),
	}

	if result := db.Where("task_pod_uuid = ?", &taskLog.TaskPodUUID).FirstOrCreate(&taskLog); result.Error != nil {
		return fmt.Errorf("failed to save task log: %+v, %+v", taskLog, result.Error)
	}

	if taskLog.Size != uint64(len([]byte(content))) {
		if taskLog.Size > uint64(len([]byte(content))) {
			return fmt.Errorf("sent log's size was smaller than previously version")
		}
		// The content has been updated. Read the bytes after what has already been written to preserve the
		// original content. We don't want to allow logs in the database to be changed once they are written.
		taskLog.Message += string([]byte(content)[taskLog.Size:])
		taskLog.Size = uint64(len([]byte(taskLog.Message)))
		if result := db.Save(&taskLog); result.Error != nil {
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
