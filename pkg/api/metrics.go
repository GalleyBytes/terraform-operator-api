package api

import (
	"net/http"

	"github.com/galleybytes/terraform-operator-api/pkg/common/models"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func approvalStatusBasedOnLastestRerunOfResource(db *gorm.DB, tfoResourceUUID, generation string) *gorm.DB {
	maxRerun := db.Table("task_pods").
		Select("MAX(rerun)").
		Where("tfo_resource_uuid = ? AND generation = ?", tfoResourceUUID, generation)

	taskPodUUID := db.Debug().Table("task_pods").
		Select("uuid").
		Where("tfo_resource_uuid = ? AND generation = ? AND  task_type = 'plan' AND rerun = (?)", tfoResourceUUID, generation, maxRerun)

	return db.Debug().Table("approvals").
		Select("*").
		Where("task_pod_uuid = (?)", taskPodUUID)
}

func highestRerunCountForTasksGeneratedForResource(db *gorm.DB, tfoResourceUUID, generation string) *gorm.DB {
	sub := db.Table("task_pods").
		Select("MAX(rerun)").
		Where("tfo_resource_uuid = ? AND generation = ?", tfoResourceUUID, generation)

	return db.Debug().Table("task_pods").
		Select("*").
		Where("tfo_resource_uuid = ? AND generation = ? AND rerun = (?)", tfoResourceUUID, generation, sub)
}

func allTasksGeneratedForResource(db *gorm.DB, tfoResourceUUID, generation string) *gorm.DB {
	return db.Debug().Table("task_pods").
		Select("*").
		Where("tfo_resource_uuid = ? and generation = ?", tfoResourceUUID, generation)
}

func resourceSpec(db *gorm.DB, uuid, generation string) *gorm.DB {
	return db.Table("tfo_resource_specs").
		Select("generation, resource_spec, annotations, labels").
		Where("tfo_resource_uuid = ? and generation = ?", uuid, generation)
}

func approvalQuery(db *gorm.DB, uuid string) *gorm.DB {
	return db.Table("approvals").
		Select("is_approved").
		Where("task_pod_uuid = ?", uuid)
}

func workflow(db *gorm.DB, clusterName uint, namespace, name string) *gorm.DB {
	return db.Table("tfo_resources").
		Select("tfo_resources.*, clusters.name AS cluster_name").
		Joins("JOIN clusters ON tfo_resources.cluster_id = clusters.id").
		Where("tfo_resources.deleted_at is null and tfo_resources.cluster_id = ? and tfo_resources.namespace = ? and tfo_resources.name = ?", clusterName, namespace, name)
}

func workflows(db *gorm.DB) *gorm.DB {
	return db.Table("tfo_resources").
		Select("tfo_resources.uuid, tfo_resources.current_generation, tfo_resources.name, tfo_resources.namespace, tfo_resources.current_state, clusters.name AS cluster_name").
		Joins("JOIN clusters ON tfo_resources.cluster_id = clusters.id").
		Where("tfo_resources.deleted_at is null")
}

func (h APIHandler) TotalResources(c *gin.Context) {
	var count int64
	workflows(h.DB).Count(&count)
	c.JSON(http.StatusOK, response(http.StatusOK, "", []int64{count}))
}

func (h APIHandler) TotalFailedResources(c *gin.Context) {
	var count int64
	var tfoResources []models.TFOResource
	h.DB.Model(&tfoResources).Where("current_state = 'failed'").Count(&count)
	c.JSON(http.StatusOK, response(http.StatusOK, "", []int64{count}))
}
