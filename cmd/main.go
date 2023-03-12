package main

import (
	"github.com/galleybytes/terraform-operator-api/internal/qworker"
	"github.com/galleybytes/terraform-operator-api/pkg/api"
	"github.com/galleybytes/terraform-operator-api/pkg/common/db"
	"github.com/gammazero/deque"
	tfv1alpha2 "github.com/isaaguilar/terraform-operator/pkg/apis/tf/v1alpha2"
	"github.com/spf13/viper"
)

func main() {
	viper.SetConfigFile("./pkg/common/envs/.env")
	viper.ReadInConfig()
	viper.AutomaticEnv()

	port := viper.Get("PORT").(string)
	dbUrl := viper.Get("DB_URL").(string)

	database := db.Init(dbUrl)
	queue := deque.Deque[tfv1alpha2.Terraform]{}

	qworker.BackgroundWorker(&queue)

	apiHandler := api.NewAPIHandler(database, &queue)
	apiHandler.RegisterRoutes()
	apiHandler.Server.Run(port)
}
