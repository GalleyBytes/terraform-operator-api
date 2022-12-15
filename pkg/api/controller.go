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
	routes.GET("/", h.Index)
	// Clusters
	routes.GET("/clusters", h.GetClusters)
	routes.GET("/cluster-name/:cluster_name", h.GeIdByClusterName)
	// Resources
	routes.GET("/cluster/:cluster_id/resources", h.GetClustersResources)
	routes.GET("/resource/:tfo_resource_uuid", h.GetResourceByUUID)
	// List Generations
	routes.GET("/resource/:tfo_resource_uuid/generations", h.GetDistinctGeneration)
	// ReourceSpec
	routes.GET("/resource/:tfo_resource_uuid/resource-spec/generation/:generation", h.GetResourceSpec)
	// Logs
	routes.GET("/resource/:tfo_resource_uuid/logs", h.GetClustersResourcesLogs)
	routes.GET("/resource/:tfo_resource_uuid/logs/generation/:generation", h.GetClustersResourcesLogs)
	routes.GET("/resource/:tfo_resource_uuid/logs/generation/:generation/task/:task_type", h.GetClustersResourcesLogs)
	routes.GET("/resource/:tfo_resource_uuid/logs/generation/:generation/task/:task_type/rerun/:rerun", h.GetClustersResourcesLogs)
	// Approval
	routes.GET("/resource/:tfo_resource_uuid/approval-status", h.GetApprovalStatus)
	routes.POST("/approval/:task_pod_uuid", h.UpdateApproval)

	// Websockets will be prefixed with /ws
	sockets := r.Group("/ws/")
	sockets.GET("/:tfo_resource_uuid", h.ResourceLogWatcher)
}
