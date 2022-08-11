package models

import (
	"gorm.io/gorm"
)

type TFOTaskLog struct {
	gorm.Model
	TaskType        string `json:"task_type"`
	Generation      string `json:"generation"`
	Rerun           int    `json:"rerun"`
	Message         string `json:"message"`
	TFOResource     TFOResource
	TFOResourceUUID string `json:"tfo_resource_uuid"`
	LineNo          string `json:"line_no"`
}

type TFOResource struct {
	UUID      string `json:"uuid" gorm:"primaryKey"`
	CreatedBy string `json:"created_by"`
	CreatedAt string `json:"created_at"`
	UpdatedBy string `json:"updated_by"`
	UpdatedAt string `json:"updated_at"`
	DeletedBy string `json:"deleted_by"`
	DeletedAt string `json:"deleeted_at"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`

	// foreign key to a cluster
	Cluster   Cluster
	ClusterID uint `json:"cluster_id"`

	CurrentGeneration string `json:"current_generation"`
}

type Cluster struct {
	gorm.Model
	Name string `json:"name" `
}

type TFOResourceSpec struct {
	gorm.Model
	TFOResource     TFOResource
	TFOResourceUUID string `json:"tfo_resource_uuid"`
	Generation      string
	ResourceSpec    string
}
