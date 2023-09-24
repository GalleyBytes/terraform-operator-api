package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/galleybytes/terraform-operator-api/pkg/api"
	"github.com/galleybytes/terraform-operator-api/pkg/common/db"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"gorm.io/gorm"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
)

var (
	port            string
	dbURL           string
	ssoLoginURL     string
	samlIssuer      string
	samlRecipient   string
	samlMetadataURL string
	useServiceHost  bool
	serviceName     string
)

func main() {
	viper.SetConfigFile("./pkg/common/envs/.env")
	viper.ReadInConfig()
	viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	viper.AutomaticEnv()

	klog.InitFlags(nil)

	pflag.CommandLine.AddGoFlag(flag.CommandLine.Lookup("v"))
	pflag.CommandLine.AddGoFlag(flag.CommandLine.Lookup("logtostderr"))
	pflag.CommandLine.Set("logtostderr", "true")
	pflag.StringVar(&port, "port", "", "Port to expose the API on")
	viper.BindPFlag("port", pflag.Lookup("port"))
	pflag.StringVar(&dbURL, "db-url", "", "Database url format (Example: 'postgres://user:password@srv:5432/db')")
	viper.BindPFlag("db-url", pflag.Lookup("db-url"))
	pflag.StringVar(&ssoLoginURL, "sso-login-url", "", "IDP Login URL ")
	viper.BindPFlag("sso-login-url", pflag.Lookup("sso-login-url"))
	pflag.StringVar(&samlIssuer, "saml-issuer", "", "Identity Provider (IDP) Issuer")
	viper.BindPFlag("saml-issuer", pflag.Lookup("saml-issuer"))
	pflag.StringVar(&samlRecipient, "saml-recipient", "", "Service Provider")
	viper.BindPFlag("saml-recipient", pflag.Lookup("saml-recipient"))
	pflag.StringVar(&samlMetadataURL, "saml-metadata-url", "", "IDP Metadata URL")
	viper.BindPFlag("saml-metadata-url", pflag.Lookup("saml-metadata-url"))
	pflag.BoolVar(&useServiceHost, "use-service-host", false, "Auto detect the ClusterIP of service for callback")
	viper.BindPFlag("use-service-host", pflag.Lookup("use-service-host"))
	pflag.StringVar(&serviceName, "service-name", "", "When `--use-service-host` will looup clusterIP of service")
	viper.BindPFlag("service-name", pflag.Lookup("service-name"))
	pflag.Parse()

	pflag.Set("alsologtostderr", "false")
	pflag.Set("logtostderr", "false")

	// goflag.Parse()
	klog.SetOutput(io.Discard)

	klog.Info("Don't show this log")
	klog.Warning("Don't show this warning")

	port = viper.GetString("port")
	dbURL = viper.GetString("db-url")
	ssoLoginURL = viper.GetString("sso-login-url")
	samlIssuer = viper.GetString("saml-issuer")
	samlRecipient = viper.GetString("saml-recipient")
	samlMetadataURL = viper.GetString("saml-metadata-url")
	useServiceHost = viper.GetBool("use-service-host")
	serviceName = viper.GetString("service-name")

	clientset := kubernetes.NewForConfigOrDie(NewConfigOrDie(os.Getenv("KUBECONFIG")))
	var database *gorm.DB
	if dbURL != "" {
		database = db.Init(dbURL)
	}

	ssoConfig, err := api.NewSAMLConfig(samlIssuer, samlRecipient, samlMetadataURL)
	if err != nil {
		log.Fatal(err)
	}
	if ssoConfig != nil {
		ssoConfig.URL = ssoLoginURL
	}

	var serviceIP string
	if useServiceHost {
		s := strings.ReplaceAll(strings.ToUpper(serviceName), "-", "_")
		serviceIP = os.Getenv(fmt.Sprintf("%s_SERVICE_HOST", s))
	}

	apiHandler := api.NewAPIHandler(database, clientset, ssoConfig, &serviceIP)
	apiHandler.RegisterRoutes()
	apiHandler.Server.Run(port)
}

func NewConfigOrDie(kubeconfigPath string) *rest.Config {
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		log.Fatal("Failed to get config for clientset")
	}
	return config
}
