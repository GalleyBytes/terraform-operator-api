package db

import (
	"log"

	"github.com/GalleyBytes/terraform-operator-api/pkg/common/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func Init(url string) *gorm.DB {
	db, err := gorm.Open(postgres.Open(url), &gorm.Config{})

	if err != nil {
		log.Fatalln(err)
	}

	db.AutoMigrate(&models.TFOResource{}, &models.TFOTaskLog{}, &models.Cluster{}, &models.TFOResourceSpec{})

	return db
}
