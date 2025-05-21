package db

import (
	"log"

	"github.com/galleybytes/infrakube-stella/pkg/common/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func Init(url string) *gorm.DB {
	db, err := gorm.Open(postgres.Open(url), &gorm.Config{})
	if err != nil {
		log.Fatalln(err)
	}

	err = db.AutoMigrate(
		&models.Infra3Resource{},
		&models.Infra3TaskLog{},
		&models.Cluster{},
		&models.Infra3ResourceSpec{},
		&models.Approval{},
		&models.TaskPod{},
		&models.RefreshToken{},
	)

	if err != nil {
		log.Panic(err)
	}

	return db
}
