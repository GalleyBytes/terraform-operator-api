package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"

	"github.com/galleybytes/terraform-operator-api/pkg/common/models"
	"github.com/gin-gonic/gin"
	tfv1alpha2 "github.com/isaaguilar/terraform-operator/pkg/apis/tf/v1alpha2"
	"gorm.io/gorm"
)

type resource struct {
	tfv1alpha2.Terraform `json:",inline"`
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
	clusterID := c.Param("cluster_id")
	if clusterID == "" || clusterID == "0" {
		return errors.New(":cluster_id: url parameter cannot be empty or 0")
	}
	clusterIDInt, err := strconv.Atoi(clusterID)
	if err != nil {
		return errors.New(":cluster_id: url parameter must be a valid integer")
	}
	clusterIDUint := uint(clusterIDInt)

	jsonData := resource{}
	err = c.BindJSON(&jsonData)
	if err != nil {
		log.Println("Unable to parse JSON from request data")
		return errors.New("Unable to parse JSON from request data: " + err.Error())
	}

	// if tfoResource.UUID == "" {}
	err = jsonData.validate()
	if err != nil {
		return err
	}

	tfoResource, tfoResourceSpec, err := jsonData.Parse(clusterIDUint)
	if err != nil {
		return err
	}

	result := h.DB.First(&models.Cluster{
		Model: gorm.Model{
			ID: clusterIDUint,
		},
	})
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

	// TODO On ADD events, the tfo resource should be saved to the cluster. This should be done async. The event should
	// be added a a persistent queue. Make the queue used in this project swappable for an external queue.
	h.Queue.PushBack(jsonData.Terraform)

	return nil
}

func (h APIHandler) updateResource(c *gin.Context) error {
	clusterID := c.Param("cluster_id")
	if clusterID == "" || clusterID == "0" {
		return errors.New(":cluster_id: url parameter cannot be empty or 0")
	}
	clusterIDInt, err := strconv.Atoi(clusterID)
	if err != nil {
		return errors.New(":cluster_id: url parameter must be a valid integer")
	}
	clusterIDUint := uint(clusterIDInt)

	jsonData := resource{}
	err = c.BindJSON(&jsonData)
	if err != nil {
		log.Println("Unable to parse JSON from request data")
		return errors.New("Unable to parse JSON from request data: " + err.Error())
	}

	// if tfoResource.UUID == "" {}
	err = jsonData.validate()
	if err != nil {
		return err
	}

	tfoResource, tfoResourceSpec, err := jsonData.Parse(clusterIDUint)
	if err != nil {
		return err
	}

	result := h.DB.First(&models.Cluster{
		Model: gorm.Model{
			ID: clusterIDUint,
		},
	})
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

	// TODO On UPDATE events, the tfo resource should be saved to the cluster. This should be done async. The event should
	// be added a a persistent queue. Make the queue used in this project swappable for an external queue.
	h.Queue.PushBack(jsonData.Terraform)

	return nil
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
