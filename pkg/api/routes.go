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

	login := r.Group("/")
	login.POST("/login", h.login)

	routes := r.Group("/api/v1/")
	routes.Use(validateJwt)
	routes.GET("/", h.Index)
	// Clusters
	routes.POST("/cluster", h.AddCluster)
	routes.GET("/clusters", h.ListClusters)
	routes.GET("/cluster/:cluster_id", h.GetCluster)
	routes.GET("/cluster-name/:cluster_name", h.GetCluster)
	// Resources
	routes.POST("/resource", h.AddTFOResource)
	routes.PUT("/resource", h.UpdateTFOResource)
	routes.GET("/cluster/:cluster_id/resources", h.GetClustersResources)
	routes.GET("/resource/:tfo_resource_uuid", h.GetResourceByUUID)
	// List Generations
	routes.GET("/resource/:tfo_resource_uuid/generations", h.GetDistinctGeneration)
	// ReourceSpec
	routes.POST("/resource-spec", h.AddTFOResourceSpec)
	routes.GET("/resource/:tfo_resource_uuid/resource-spec/generation/:generation", h.GetResourceSpec)
	// Logs
	routes.POST("/logs", h.AddTFOTaskLogs)
	routes.GET("/resource/:tfo_resource_uuid/logs", h.GetClustersResourcesLogs)
	routes.GET("/resource/:tfo_resource_uuid/logs/generation/:generation", h.GetClustersResourcesLogs)
	routes.GET("/resource/:tfo_resource_uuid/logs/generation/:generation/task/:task_type", h.GetClustersResourcesLogs)
	routes.GET("/resource/:tfo_resource_uuid/logs/generation/:generation/task/:task_type/rerun/:rerun", h.GetClustersResourcesLogs)
	routes.GET("/task/:task_pod_uuid/logs", h.GetTFOTaskLogsViaTask)
	// Tasks
	routes.POST("/task", h.AddTaskPod)
	routes.GET("/task/:task_pod_uuid", h.GetTaskPod)
	// Approval
	routes.GET("/resource/:tfo_resource_uuid/approval-status", h.GetApprovalStatus)
	routes.GET("/task/:task_pod_uuid/approval-status", h.GetApprovalStatusViaTaskPodUUID)
	routes.POST("/approval/:task_pod_uuid", h.UpdateApproval)
	routes.GET("/approvals", h.AllApprovals)

	// Websockets will be prefixed with /ws
	sockets := r.Group("/ws/")
	sockets.GET("/:tfo_resource_uuid", h.ResourceLogWatcher)
}
