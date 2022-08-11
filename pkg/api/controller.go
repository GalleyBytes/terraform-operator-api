package api

import (
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type handler struct {
	DB *gorm.DB
}

func RegisterRoutes(r *gin.Engine, db *gorm.DB) {
	h := &handler{
		DB: db,
	}

	routes := r.Group("/api/v1/")
	routes.GET("/", h.GetRecords)
	routes.GET("/clusters", h.GetClusters)
	routes.GET("/cluster/:cluster_id/resources", h.GetClustersResources)
	routes.GET("/cluster_id/:cluster_id", h.GetUuidByClusterID)
	routes.GET("/resource/:tfo_resource_uuid/logs/generation/:generation", h.GetClustersResourcesLogs)
	routes.GET("/resource/:tfo_resource_uuid/generations", h.GetDistinctGeneration)
	routes.GET("/cluster_name/:cluster_name", h.GeIdByClusterName)
	routes.GET("/:tfo_resource_uuid", h.GetLog)
	routes.GET("/logs_by_generation/:tfo_resource_uuid/:generation", h.GetLogByGeneration)
	routes.GET("/resource/:tfo_resource_uuid", h.GetResourceByUUID)
	routes.GET("/resource/:tfo_resource_uuid/generation/:generation/resourcespec", h.GetResourceSpec)
	routes.GET("/query_rerun/:tfo_resource_uuid/:task_type/:rerun_value/:generation", h.GetRerunByNumber)
	routes.GET("/highest_rerun/:generation", h.GetHighestRerunLog)
	routes.GET("/highest_rerun_for_tfo_resource/:tfo_resource_uuid", h.GetHighestRerunLogForTFO)
}
