package api

import (
	"log"
	"net/http"

	"github.com/galleybytes/terraform-operator-api/pkg/common/models"
	"github.com/gin-gonic/gin"
)

func (h APIHandler) AddTaskPod(c *gin.Context) {

	jsonData := struct {
		TaskPod models.TaskPod `json:"task_pod"`
	}{}
	err := c.BindJSON(&jsonData)
	if err != nil {
		log.Println(err)
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, err.Error(), nil))
		return
	}
	if jsonData.TaskPod.UUID == "" {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, "missing request data", nil))
		return
	}

	taskPod := jsonData.TaskPod
	result := h.DB.Where("uuid = ?", &taskPod.UUID).FirstOrCreate(&taskPod)
	if result.Error != nil {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, result.Error.Error(), nil))
		return
	}

	c.JSON(http.StatusOK, response(http.StatusOK, "", []models.TaskPod{taskPod}))
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
