package main

import (
	"github.com/GalleyBytes/terraform-operator-api/pkg/api"
	"github.com/GalleyBytes/terraform-operator-api/pkg/common/db"
	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
)

func main() {
	viper.SetConfigFile("./pkg/common/envs/.env")
	viper.ReadInConfig()
	viper.AutomaticEnv()

	port := viper.Get("PORT").(string)
	dbUrl := viper.Get("DB_URL").(string)

	r := gin.Default()
	h := db.Init(dbUrl)

	api.RegisterRoutes(r, h)

	r.Run(port)

}
