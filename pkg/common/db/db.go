package db

import (
	"log"

	"github.com/galleybytes/terraform-operator-api/pkg/common/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func Init(url string) *gorm.DB {
	db, err := gorm.Open(postgres.Open(url), &gorm.Config{})
	if err != nil {
		log.Fatalln(err)
	}

	err = db.AutoMigrate(
		&models.TFOResource{},
		&models.TFOTaskLog{},
		&models.Cluster{},
		&models.TFOResourceSpec{},
		&models.Approval{},
		&models.TaskPod{},
	)

	if err != nil {
		log.Panic(err)
	}

	return db
}
