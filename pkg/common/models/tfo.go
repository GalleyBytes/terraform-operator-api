package models

import (
	"time"

	"gorm.io/gorm"
)

type TFOTaskLog struct {
	gorm.Model
	TaskPod         TaskPod     `json:"task_pod,omitempty"`
	TaskPodUUID     string      `json:"task_pod_uuid"`
	TFOResource     TFOResource `json:"tfo_resource,omitempty"`
	TFOResourceUUID string      `json:"tfo_resource_uuid"`
	Message         string      `json:"message"`
	LineNo          string      `json:"line_no"`
}

type TFOResource struct {
	UUID              string    `json:"uuid" gorm:"primaryKey"`
	CreatedBy         string    `json:"created_by"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedBy         string    `json:"updated_by"`
	UpdatedAt         time.Time `json:"updated_at"`
	DeletedBy         string    `json:"deleted_by"`
	DeletedAt         time.Time `json:"deleted_at"`
	Namespace         string    `json:"namespace"`
	Name              string    `json:"name"`
	CurrentGeneration string    `json:"current_generation"`

	// foreign key to a cluster
	Cluster   Cluster `json:"cluster,omitempty"`
	ClusterID uint    `json:"cluster_id"`
}

type Cluster struct {
	gorm.Model
	Name string `json:"name" `
}

type TFOResourceSpec struct {
	gorm.Model
	TFOResource     TFOResource `json:"tfo_resource,omitempty"`
	TFOResourceUUID string      `json:"tfo_resource_uuid"`
	Generation      string      `json:"generation"`
	ResourceSpec    string      `json:"resource_spec"`
}

type TaskPod struct {
	UUID            string      `json:"uuid" gorm:"primaryKey"`
	TaskType        string      `json:"task_type"`
	Rerun           int         `json:"rerun"`
	Generation      string      `json:"generation"`
	TFOResource     TFOResource `json:"tfo_resource,omitempty"`
	TFOResourceUUID string      `json:"tfo_resource_uuid"`
}

type Approval struct {
	gorm.Model
	IsApproved  bool    `json:"is_approved"`
	TaskPod     TaskPod `json:"task_pod,omitempty"`
	TaskPodUUID string  `json:"task_pod_uuid"`
}
