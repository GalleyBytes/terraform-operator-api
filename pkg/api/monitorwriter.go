package api

import (
	"errors"
	"log"
	"net/http"

	"github.com/galleybytes/terraform-operator-api/pkg/common/models"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func (h handler) AddCluster(c *gin.Context) {

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

	c.JSON(http.StatusOK, response(http.StatusOK, "", []models.Cluster{cluster}))
}

func (h handler) AddTFOResourceSpec(c *gin.Context) {

	jsonData := struct {
		TFOResourceSpec models.TFOResourceSpec `json:"tfo_resource_spec"`
	}{}
	err := c.BindJSON(&jsonData)
	if err != nil {
		log.Println(err)
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, err.Error(), nil))
		return
	}
	if jsonData.TFOResourceSpec.TFOResourceUUID == "" && jsonData.TFOResourceSpec.Generation == "" {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, "missing request data", nil))
		return
	}

	tfoResourceSpec := models.TFOResourceSpec{}
	if result := h.DB.Where("tfo_resource_uuid = ? AND generation = ?", &jsonData.TFOResourceSpec.TFOResourceUUID, &jsonData.TFOResourceSpec.Generation).First(&tfoResourceSpec); result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			tfoResourceSpec = jsonData.TFOResourceSpec
			result := h.DB.Create(&tfoResourceSpec)
			if result.Error != nil {
				c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, result.Error.Error(), nil))
				return
			}
		} else {
			c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, result.Error.Error(), nil))
			return
		}
	}

	c.JSON(http.StatusOK, response(http.StatusOK, "", []models.TFOResourceSpec{tfoResourceSpec}))
}

func (h handler) AddTFOResource(c *gin.Context) {

	jsonData := struct {
		TFOResource models.TFOResource `json:"tfo_resource"`
	}{}
	err := c.BindJSON(&jsonData)
	if err != nil {
		log.Println(err)
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, err.Error(), nil))
		return
	}
	if jsonData.TFOResource.UUID == "" {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, "missing request data", nil))
		return
	}

	tfoResource := jsonData.TFOResource
	result := h.DB.Where("uuid = ?", &tfoResource.UUID).FirstOrCreate(&tfoResource)
	if result.Error != nil {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, result.Error.Error(), nil))
		return
	}

	c.JSON(http.StatusOK, response(http.StatusOK, "", []models.TFOResource{tfoResource}))
}

func (h handler) UpdateTFOResource(c *gin.Context) {

	jsonData := struct {
		TFOResource models.TFOResource `json:"tfo_resource"`
	}{}
	err := c.BindJSON(&jsonData)
	if err != nil {
		log.Println(err)
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, err.Error(), nil))
		return
	}
	if jsonData.TFOResource.UUID == "" {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, "missing request data", nil))
		return
	}

	result := h.DB.Save(&jsonData.TFOResource)
	if result.Error != nil {
		c.JSON(http.StatusUnprocessableEntity, response(http.StatusUnprocessableEntity, result.Error.Error(), nil))
		return
	}

	c.JSON(http.StatusOK, response(http.StatusOK, "", []models.TFOResource{jsonData.TFOResource}))
}

func (h handler) AddTaskPod(c *gin.Context) {

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

func (h handler) AddTFOTaskLogs(c *gin.Context) {
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
