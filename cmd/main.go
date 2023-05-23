package main

import (
	"log"
	"os"

	"github.com/galleybytes/terraform-operator-api/internal/qworker"
	"github.com/galleybytes/terraform-operator-api/pkg/api"
	"github.com/galleybytes/terraform-operator-api/pkg/common/db"
	"github.com/gammazero/deque"
	tfv1alpha2 "github.com/isaaguilar/terraform-operator/pkg/apis/tf/v1alpha2"
	tfo "github.com/isaaguilar/terraform-operator/pkg/client/clientset/versioned"
	"github.com/spf13/viper"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	viper.SetConfigFile("./pkg/common/envs/.env")
	viper.ReadInConfig()
	viper.AutomaticEnv()

	port := viper.Get("PORT").(string)
	dbUrl := viper.Get("DB_URL").(string)

	clientset := kubernetes.NewForConfigOrDie(NewConfigOrDie(os.Getenv("KUBECONFIG")))
	tfoclientset := tfo.NewForConfigOrDie(NewConfigOrDie(os.Getenv("KUBECONFIG")))
	database := db.Init(dbUrl)
	queue := deque.Deque[tfv1alpha2.Terraform]{}

	qworker.BackgroundWorker(&queue)

	apiHandler := api.NewAPIHandler(database, &queue, clientset, tfoclientset)
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
