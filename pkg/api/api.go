package api

import (
	"crypto/x509"
	"fmt"

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
	ssoConfig *SSOConfig
	tenant    string
}

type SSOConfig struct {
	URL  string
	saml *SAMLOptions
}

type SAMLOptions struct {
	issuer    string
	crt       *x509.Certificate
	recipient string
}

func NewSAMLConfig(issuer, recipient, metadataURL string) (*SSOConfig, error) {
	if issuer == "" || recipient == "" || metadataURL == "" {
		return nil, nil
	}

	crt, err := fetchIDPCertificate(metadataURL)
	if err != nil {
		return nil, err
	}
	if crt == nil {
		return nil, fmt.Errorf("could not get certification from metadata url")
	}
	return &SSOConfig{
		saml: &SAMLOptions{
			issuer:    issuer,
			recipient: recipient,
			crt:       crt,
		},
	}, nil
}

func NewAPIHandler(db *gorm.DB, queue *deque.Deque[tfv1beta1.Terraform], clientset kubernetes.Interface, ssoConfig *SSOConfig) *APIHandler {

	return &APIHandler{
		Server:    gin.Default(),
		DB:        db,
		Queue:     queue,
		clientset: clientset,
		ssoConfig: ssoConfig,
	}
}

func (h APIHandler) RegisterRoutes() {
	auth := h.Server.Group("/")
	auth.POST("/login", h.login)
	auth.GET("/connect", h.defaultConnectMethod) // Determine preferred auth method
	auth.GET("/sso", h.ssoRedirecter)
	auth.POST("/sso/saml", h.samlConnecter)

	routes := h.Server.Group("/api/v1/")
	routes.Use(validateJwt)
	routes.GET("/", h.Index)

	cluster := routes.Group("/cluster")
	cluster.POST("/", h.AddCluster) // Resource from Add/Update/Delete event
	cluster.GET("/:cluster_name/health", h.VClusterHealth)
	cluster.GET("/:cluster_name/tfohealth", h.VClusterTFOHealth)
	cluster.PUT("/:cluster_name/sync-dependencies", h.SyncEvent)
	cluster.POST("/:cluster_name/event", h.ResourceEvent) // routes.GET("/cluster-name/:cluster_name", h.GetCluster) // to be removed
	cluster.PUT("/:cluster_name/event", h.ResourceEvent)
	cluster.GET("/:cluster_name/resource/:namespace/:name/poll", h.ResourcePoll) // Poll for resource objects in the cluster
	cluster.DELETE("/:cluster_name/event/:tfo_resource_uuid", h.ResourceEvent)
	cluster.GET("/:cluster_name/resource/:namespace/:name/debug", h.Debugger)
	cluster.GET("/:cluster_name/debug/:namespace/:name", h.Debugger) // Alias
	cluster.GET("/:cluster_name/resource/:namespace/:name/status", h.ResourceStatusCheck)
	cluster.GET("/:cluster_name/status/:namespace/:name", h.ResourceStatusCheck) // Alias
	cluster.GET("/:cluster_name/resource/:namespace/:name/last-task-log", h.LastTaskLog)

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
