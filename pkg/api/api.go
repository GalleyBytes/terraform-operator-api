package api

import (
	tfv1beta1 "github.com/galleybytes/terraform-operator/pkg/apis/tf/v1beta1"
	"github.com/gammazero/deque"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"k8s.io/client-go/kubernetes"
)

type APIHandler struct {
	Server    *gin.Engine
	DB        *gorm.DB
	Queue     *deque.Deque[tfv1beta1.Terraform]
	clientset kubernetes.Interface
}

func NewAPIHandler(db *gorm.DB, queue *deque.Deque[tfv1beta1.Terraform], clientset kubernetes.Interface) *APIHandler {
	return &APIHandler{
		Server:    gin.Default(),
		DB:        db,
		Queue:     queue,
		clientset: clientset,
	}
}

func (h APIHandler) RegisterRoutes() {
	login := h.Server.Group("/")
	login.POST("/login", h.login)

	routes := h.Server.Group("/api/v1/")
	routes.Use(validateJwt)
	routes.GET("/", h.Index)

	cluster := routes.Group("/cluster")
	cluster.POST("/", h.AddCluster) // Resource from Add/Update/Delete event
	cluster.PUT("/:cluster_name/sync-dependencies", h.SyncEvent)
	cluster.POST("/:cluster_name/event", h.ResourceEvent) // routes.GET("/cluster-name/:cluster_name", h.GetCluster) // to be removed
	cluster.PUT("/:cluster_name/event", h.ResourceEvent)
	cluster.GET("/:cluster_name/resource/:namespace/:name/poll", h.ResourcePoll) // Poll for resource objects in the cluster
	cluster.DELETE("/:cluster_name/event/:tfo_resource_uuid", h.ResourceEvent)

	// DEPRECATED usage of clusterid is being removed. todo ensure galleybytes projects aren't using this
	clusterid := routes.Group("/cluster-id")
	clusterid.GET("/:cluster_id", h.GetCluster)
	clusterid.GET("/:cluster_id/resources", h.GetClustersResources) // List Resources

	// List Clusters
	routes.GET("/clusters", h.ListClusters)
	routes.GET("/resource/:tfo_resource_uuid", h.GetResourceByUUID)
	// List Generations
	routes.GET("/resource/:tfo_resource_uuid/generations", h.GetDistinctGeneration)
	// ReourceSpec
	routes.GET("/resource/:tfo_resource_uuid/resource-spec/generation/:generation", h.GetResourceSpec)
	// // Poll for resource objects in the cluster
	// routes.GET("/resource/:tfo_resource_uuid/poll", h.ResourcePoll)
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
