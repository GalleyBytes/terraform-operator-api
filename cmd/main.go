package main

import (
	"log"
	"os"

	"github.com/galleybytes/terraform-operator-api/internal/qworker"
	"github.com/galleybytes/terraform-operator-api/pkg/api"
	"github.com/galleybytes/terraform-operator-api/pkg/common/db"
	tfv1beta1 "github.com/galleybytes/terraform-operator/pkg/apis/tf/v1beta1"
	"github.com/gammazero/deque"
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
	database := db.Init(dbUrl)
	queue := deque.Deque[tfv1beta1.Terraform]{}

	qworker.BackgroundWorker(&queue)

	apiHandler := api.NewAPIHandler(database, &queue, clientset)
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
