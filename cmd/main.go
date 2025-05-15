package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/galleybytes/infra3-stella/pkg/api"
	"github.com/galleybytes/infra3-stella/pkg/common/db"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"gorm.io/gorm"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
)

var (
	addr            string
	dbURL           string
	ssoLoginURL     string
	samlIssuer      string
	samlRecipient   string
	samlMetadataURL string
	useServiceHost  bool
	serviceName     string
	dashboard       string
	fswatchImage    string
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
	pflag.StringVar(&addr, "addr", "", "Address to expose the API on")
	viper.BindPFlag("addr", pflag.Lookup("addr"))
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
	pflag.StringVar(&dashboard, "dashboard", "", "Connect to the dashboard with api credentials")
	viper.BindPFlag("dashboard", pflag.Lookup("dashboard"))
	pflag.StringVar(&fswatchImage, "fswatch-image", "ghcr.io/galleybytes/fswatch:0.11.0", "Docker image for fswatch (log-service)")
	viper.BindPFlag("fswatch-image", pflag.Lookup("fswatch-image"))
	pflag.Parse()

	pflag.Set("alsologtostderr", "false")
	pflag.Set("logtostderr", "false")

	// goflag.Parse()
	klog.SetOutput(io.Discard)

	klog.Info("Don't show this log")
	klog.Warning("Don't show this warning")

	addr = viper.GetString("addr")
	dbURL = viper.GetString("db-url")
	ssoLoginURL = viper.GetString("sso-login-url")
	samlIssuer = viper.GetString("saml-issuer")
	samlRecipient = viper.GetString("saml-recipient")
	samlMetadataURL = viper.GetString("saml-metadata-url")
	useServiceHost = viper.GetBool("use-service-host")
	serviceName = viper.GetString("service-name")
	dashboard = viper.GetString("dashboard")
	fswatchImage = viper.GetString("fswatch-image")

	clientset := kubernetes.NewForConfigOrDie(NewConfigOrDie(os.Getenv("KUBECONFIG")))
	var database *gorm.DB
	if dbURL != "" {
		database = db.Init(dbURL)
	}

	if addr == "" {
		addr = ":3000"
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
		servicePort := os.Getenv(fmt.Sprintf("%s_SERVICE_PORT", s))
		if servicePort != "" {
			serviceIP += ":" + servicePort
		}
	}

	apiHandler := api.NewAPIHandler(database, clientset, ssoConfig, &serviceIP, &dashboard, fswatchImage)
	apiHandler.RegisterRoutes()
	fmt.Printf("Starting server on %s\n", addr)
	apiHandler.Server.Run(addr)
}

func NewConfigOrDie(kubeconfigPath string) *rest.Config {
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		log.Fatal("Failed to get config for clientset")
	}
	return config
}
