package api

import (
	"github.com/gammazero/deque"
	"github.com/gin-gonic/gin"
	tfv1alpha2 "github.com/isaaguilar/terraform-operator/pkg/apis/tf/v1alpha2"
	"gorm.io/gorm"
)

type APIHandler struct {
	Server *gin.Engine
	DB     *gorm.DB
	Queue  *deque.Deque[tfv1alpha2.Terraform]
}

func NewAPIHandler(db *gorm.DB, queue *deque.Deque[tfv1alpha2.Terraform]) *APIHandler {
	return &APIHandler{
		Server: gin.Default(),
		DB:     db,
		Queue:  queue,
	}
}

func (h APIHandler) RegisterRoutes() {
	login := h.Server.Group("/")
	login.POST("/login", h.login)

	routes := h.Server.Group("/api/v1/")
	routes.Use(validateJwt)
	routes.GET("/", h.Index)
	// Resource from Add/Update/Delete event
	routes.POST("/cluster/:cluster_id/event", h.ResourceEvent)
	routes.PUT("/cluster/:cluster_id/event", h.ResourceEvent)
	routes.DELETE("/cluster/:cluster_id/event/:tfo_resource_uuid", h.ResourceEvent)
	// Clusters
	routes.POST("/cluster", h.AddCluster)
	routes.GET("/clusters", h.ListClusters)
	routes.GET("/cluster/:cluster_id", h.GetCluster)
	routes.GET("/cluster-name/:cluster_name", h.GetCluster)
	// Resources
	routes.GET("/cluster/:cluster_id/resources", h.GetClustersResources)
	routes.GET("/resource/:tfo_resource_uuid", h.GetResourceByUUID)
	// List Generations
	routes.GET("/resource/:tfo_resource_uuid/generations", h.GetDistinctGeneration)
	// ReourceSpec
	routes.GET("/resource/:tfo_resource_uuid/resource-spec/generation/:generation", h.GetResourceSpec)
	// Logs
	routes.POST("/logs", h.AddTFOTaskLogs) // requires access to fs of logs
	routes.GET("/resource/:tfo_resource_uuid/logs", h.GetClustersResourcesLogs)
	routes.GET("/resource/:tfo_resource_uuid/logs/generation/:generation", h.GetClustersResourcesLogs)
	routes.GET("/resource/:tfo_resource_uuid/logs/generation/:generation/task/:task_type", h.GetClustersResourcesLogs)
	routes.GET("/resource/:tfo_resource_uuid/logs/generation/:generation/task/:task_type/rerun/:rerun", h.GetClustersResourcesLogs)
	routes.GET("/task/:task_pod_uuid/logs", h.GetTFOTaskLogsViaTask)
	// Tasks
	routes.POST("/task", h.AddTaskPod) // requires access to fs of logs
	routes.GET("/task/:task_pod_uuid", h.GetTaskPod)
	// Approval
	routes.GET("/resource/:tfo_resource_uuid/approval-status", h.GetApprovalStatus)
	routes.GET("/task/:task_pod_uuid/approval-status", h.GetApprovalStatusViaTaskPodUUID)
	routes.POST("/approval/:task_pod_uuid", h.UpdateApproval)
	routes.GET("/approvals", h.AllApprovals)

	// Websockets will be prefixed with /ws
	sockets := h.Server.Group("/ws/")
	sockets.GET("/:tfo_resource_uuid", h.ResourceLogWatcher)
}
