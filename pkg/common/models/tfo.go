package models

import (
	"time"

	"gorm.io/gorm"
)

type TFOTaskLog struct {
	gorm.Model
	TaskPod     TaskPod `json:"task_pod,omitempty"`
	TaskPodUUID string  `json:"task_pod_uuid"`
	Message     string  `json:"message" gorm:"type:varchar(1048576)"`
	Size        uint64  `json:"size"`
}

type TFOResource struct {
	UUID              string         `json:"uuid" gorm:"primaryKey"`
	CreatedBy         string         `json:"created_by"`
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedBy         string         `json:"updated_by"`
	UpdatedAt         time.Time      `json:"updated_at"`
	DeletedBy         string         `json:"deleted_by"`
	DeletedAt         gorm.DeletedAt `gorm:"index" json:"deleted_at"`
	Namespace         string         `json:"namespace"`
	Name              string         `json:"name"`
	CurrentGeneration string         `json:"current_generation"`
	CurrentState      ResourceState  `json:"current_state"`

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
	TaskToken       string      `json:"task_token"`
	Annotations     string      `json:"annotations"`
	Labels          string      `json:"labels"`
}

type TaskPod struct {
	UUID                string      `json:"uuid" gorm:"primaryKey"`
	TaskType            string      `json:"task_type"`
	Rerun               int         `json:"rerun"`
	Generation          string      `json:"generation"`
	InClusterGeneration string      `json:"in_cluster_generation"`
	TFOResource         TFOResource `json:"tfo_resource,omitempty"`
	TFOResourceUUID     string      `json:"tfo_resource_uuid"`
}

type Approval struct {
	gorm.Model
	IsApproved  bool    `json:"is_approved"`
	TaskPod     TaskPod `json:"task_pod,omitempty"`
	TaskPodUUID string  `json:"task_pod_uuid"`
}

type ResourceState string

type RefreshToken struct {
	gorm.Model
	RefreshToken   string
	Version        int
	UsedAt         *time.Time
	ReUsedAt       *time.Time
	CanceledAt     *time.Time
	CanceledReason string

	TFOResourceSpec   TFOResourceSpec `json:"tfo_resource_spec,omitempty"`
	TFOResourceSpecID uint            `json:"tfo_resource_spec_id"`
}

const (
	Untracked ResourceState = "untracked"
	Running   ResourceState = "running"
	Failed    ResourceState = "failed"
	Completed ResourceState = "completed"
)
