package api

import (
	tfv1beta1 "github.com/galleybytes/terraform-operator/pkg/apis/tf/v1beta1"
	tfo "github.com/galleybytes/terraform-operator/pkg/client/clientset/versioned"
	"github.com/gammazero/deque"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type APIHandler struct {
	Server       *gin.Engine
	DB           *gorm.DB
	Queue        *deque.Deque[tfv1beta1.Terraform]
	config       *rest.Config
	clientset    kubernetes.Interface
	tfoclientset tfo.Interface
}

func NewAPIHandler(db *gorm.DB, queue *deque.Deque[tfv1beta1.Terraform], clientset kubernetes.Interface, tfoclientset tfo.Interface) *APIHandler {
	return &APIHandler{
		Server:       gin.Default(),
		DB:           db,
		Queue:        queue,
		clientset:    clientset,
		tfoclientset: tfoclientset,
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
	cluster.GET("/:cluster_id", h.GetCluster)
	cluster.POST("/:cluster_name/event", h.ResourceEvent) // routes.GET("/cluster-name/:cluster_name", h.GetCluster) // to be removed
	cluster.PUT("/:cluster_name/event", h.ResourceEvent)
	cluster.GET("/:cluster_id/resources", h.GetClustersResources) // List Resources
	cluster.DELETE("/:cluster_name/event/:tfo_resource_uuid", h.ResourceEvent)

	// List Clusters
	routes.GET("/clusters", h.ListClusters)
	routes.GET("/resource/:tfo_resource_uuid", h.GetResourceByUUID)
	// List Generations
	routes.GET("/resource/:tfo_resource_uuid/generations", h.GetDistinctGeneration)
	// ReourceSpec
	routes.GET("/resource/:tfo_resource_uuid/resource-spec/generation/:generation", h.GetResourceSpec)
	// Poll for resource objects in the cluster
	routes.GET("/resource/:tfo_resource_uuid/poll", h.ResourcePoll)
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
