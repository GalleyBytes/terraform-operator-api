package api

import (
	"crypto/x509"
	"fmt"
	"net/http"

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
	serviceIP *string
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

func NewAPIHandler(db *gorm.DB, queue *deque.Deque[tfv1beta1.Terraform], clientset kubernetes.Interface, ssoConfig *SSOConfig, serviceIP *string) *APIHandler {

	return &APIHandler{
		Server:    gin.Default(),
		DB:        db,
		Queue:     queue,
		clientset: clientset,
		ssoConfig: ssoConfig,
		serviceIP: serviceIP,
	}
}

func (h APIHandler) RegisterRoutes() {
	preauth := h.Server.Group("/")
	preauth.GET("/noauthtest", func(c *gin.Context) {
		c.JSON(200, response(200, "", []string{"Please come again!"}))
	})
	preauth.POST("/noauthtest", func(c *gin.Context) {
		var data any
		err := c.BindJSON(&data)
		if err != nil {
			c.AbortWithError(http.StatusNotAcceptable, err)
			return
		}
		c.JSON(200, response(200, "", []any{data}))
	})

	preauth.POST("/login", h.login)
	preauth.GET("/connect", h.defaultConnectMethod) // Determine preferred auth method
	preauth.GET("/sso", h.ssoRedirecter)
	preauth.POST("/sso/saml", h.samlConnecter)

	authenticatedAPIV1 := h.Server.Group("/api/v1/")
	authenticatedAPIV1.Use(validateJwt)
	authenticatedAPIV1.GET("/", h.Index)

	cluster := authenticatedAPIV1.Group("/cluster")
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

	// DEPRECATED usage of clusterid is being removed. todo ensure galleybytes projects aren't using this
	clusterid := authenticatedAPIV1.Group("/cluster-id")
	clusterid.GET("/:cluster_id", h.GetCluster)
	clusterid.GET("/:cluster_id/resources", h.GetClustersResources) // List Resources

	// List Clusters
	authenticatedAPIV1.GET("/clusters", h.ListClusters)
	authenticatedAPIV1.GET("/resource/:tfo_resource_uuid", h.GetResourceByUUID)
	// List Generations
	authenticatedAPIV1.GET("/resource/:tfo_resource_uuid/generations", h.GetDistinctGeneration)
	// ReourceSpec
	authenticatedAPIV1.GET("/resource/:tfo_resource_uuid/resource-spec/generation/:generation", h.GetResourceSpec)

	authenticatedAPIV1.GET("/resource/:tfo_resource_uuid/logs", h.GetClustersResourcesLogs)
	authenticatedAPIV1.GET("/resource/:tfo_resource_uuid/logs/generation/:generation", h.GetClustersResourcesLogs)
	authenticatedAPIV1.GET("/resource/:tfo_resource_uuid/logs/generation/:generation/task/:task_type", h.GetClustersResourcesLogs)
	authenticatedAPIV1.GET("/resource/:tfo_resource_uuid/logs/generation/:generation/task/:task_type/rerun/:rerun", h.GetClustersResourcesLogs)
	authenticatedAPIV1.GET("/task/:task_pod_uuid/logs", h.GetTFOTaskLogsViaTask)
	authenticatedAPIV1.GET("/task/:task_pod_uuid", h.GetTaskPod) // TODO Should getting a task out of band (ie not with cluster info) be allowed?

	// Tasks via task JWT
	authenticatedTask := h.Server.Group("/api/v1/task")
	authenticatedTask.Use(validateTaskJWT)
	authenticatedTask.POST("", h.AddTaskPod)

	// Approval
	authenticatedAPIV1.GET("/resource/:tfo_resource_uuid/approval-status", h.GetApprovalStatus)
	authenticatedAPIV1.GET("/task/:task_pod_uuid/approval-status", h.GetApprovalStatusViaTaskPodUUID)
	authenticatedAPIV1.POST("/approval/:task_pod_uuid", h.UpdateApproval)
	authenticatedAPIV1.GET("/approvals", h.AllApprovals)

	// Websockets will be prefixed with /ws
	sockets := h.Server.Group("/ws/")
	sockets.GET("/:tfo_resource_uuid", h.ResourceLogWatcher)
}
