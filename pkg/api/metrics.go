package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/galleybytes/infra3-stella/pkg/common/models"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func resourceLog(db *gorm.DB, taskUUID string) *gorm.DB {
	return db.Table("infra3_task_logs").
		Select("message, updated_at, created_at").
		Where("task_pod_uuid = ?", taskUUID)
}

func requiredApprovalPodUUID(db *gorm.DB, infra3ResourceUUID, generation string) *gorm.DB {
	maxRerun := db.Table("task_pods").
		Select("MAX(rerun)").
		Where("infra3_resource_uuid = ? AND generation = ?", infra3ResourceUUID, generation)

	maxInClusterGeneration := db.Table("task_pods").
		Select("MAX(in_cluster_generation)").
		Where("infra3_resource_uuid = ? AND generation = ? AND rerun = (?)", infra3ResourceUUID, generation, maxRerun)

	return db.Table("task_pods").
		Select("uuid").
		Where("infra3_resource_uuid = ? AND generation = ? AND  task_type = 'plan' AND rerun = (?) and in_cluster_generation = (?)", infra3ResourceUUID, generation, maxRerun, maxInClusterGeneration)

}

func approvalStatusBasedOnLastestRerunOfResource(db *gorm.DB, infra3ResourceUUID, generation string) *gorm.DB {
	taskPodUUID := requiredApprovalPodUUID(db, infra3ResourceUUID, generation)
	return db.Debug().Table("approvals").
		Select("*").
		Where("task_pod_uuid = (?)", taskPodUUID)
}

func highestRerunCountForTasksGeneratedForResource(db *gorm.DB, infra3ResourceUUID, generation string) *gorm.DB {
	sub := db.Table("task_pods").
		Select("MAX(rerun)").
		Where("infra3_resource_uuid = ? AND generation = ?", infra3ResourceUUID, generation)

	return db.Debug().Table("task_pods").
		Select("*").
		Where("infra3_resource_uuid = ? AND generation = ? AND rerun = (?)", infra3ResourceUUID, generation, sub)
}

func allTasksGeneratedForResource(db *gorm.DB, infra3ResourceUUID, generation string) *gorm.DB {
	return db.Debug().Table("task_pods").
		Select("*").
		Where("infra3_resource_uuid = ? and generation = ?", infra3ResourceUUID, generation)
}

func resourceSpec(db *gorm.DB, uuid, generation string) *gorm.DB {
	return db.Table("infra3_resource_specs").
		Select(`
			generation,
			resource_spec,
			annotations,
			labels,
			created_at,
			updated_at
		`).
		Where("infra3_resource_uuid = ? and generation = ?", uuid, generation)
}

func approvalQuery(db *gorm.DB, uuid string) *gorm.DB {
	return db.Table("approvals").
		Select("is_approved").
		Where("task_pod_uuid = ?", uuid)
}

func workflow(db *gorm.DB, clusterName uint, namespace, name string) *gorm.DB {
	return db.Table("infra3_resources").
		Select(`
			infra3_resources.*,
			clusters.name AS cluster_name
		`).
		Joins("JOIN clusters ON infra3_resources.cluster_id = clusters.id").
		Where("infra3_resources.deleted_at is null and infra3_resources.cluster_id = ? and infra3_resources.namespace = ? and infra3_resources.name = ?", clusterName, namespace, name)
}

func workflows(db *gorm.DB, name, namespace, clusterName string, offset, limit int) *gorm.DB {
	queryString := fmt.Sprintf(`
		SELECT
			infra3_resources.uuid,
			infra3_resources.current_generation,
			infra3_resources.name,
			infra3_resources.namespace,
			infra3_resources.current_state,
			infra3_resources.created_at,
			clusters.name as cluster_name,
			infra3_resources.updated_at as resource_updated_at,
			logs.updated_at as updated_at
		FROM infra3_resources
		LEFT JOIN (
			SELECT task_pods.infra3_resource_uuid, MAX(infra3_task_logs.updated_at) as updated_at
			FROM infra3_task_logs
			JOIN task_pods on task_pods.uuid = infra3_task_logs.task_pod_uuid
			WHERE infra3_task_logs.updated_at IS NOT NULL
			GROUP BY task_pods.infra3_resource_uuid
		) logs ON logs.infra3_resource_uuid = infra3_resources.uuid
		JOIN clusters ON clusters.id = infra3_resources.cluster_id
		WHERE infra3_resources.deleted_at IS NULL
		AND infra3_resources.name LIKE '%%%s%%'
		AND infra3_resources.namespace LIKE '%%%s%%'
		AND clusters.name LIKE '%%%s%%'
		ORDER BY logs.updated_at DESC NULLS LAST
		OFFSET %d
		LIMIT %d
	`, name, namespace, clusterName, offset, limit)

	return db.Raw(queryString)
}

func (h APIHandler) TotalResources(c *gin.Context) {

	name := ""
	namespace := ""
	clusterName := ""
	matchAny, _ := c.GetQuery("matchAny")
	if matchAny != "" {
		m := fmt.Sprintf("%%%s%%", matchAny)
		name = m
		namespace = m
		clusterName = m

		if strings.Contains(matchAny, "=") {
			name = "%"
			namespace = "%"
			clusterName = "%"
			for _, matchAnyOfColumn := range strings.Split(matchAny, " ") {
				if !strings.Contains(matchAnyOfColumn, "=") {
					continue
				}
				columnQuery := strings.Split(matchAnyOfColumn, "=")
				key := columnQuery[0]
				value := columnQuery[1]
				if key == "name" {
					name = value
				}
				if key == "namespace" {
					namespace = value
				}
				if strings.HasPrefix(key, "cluster") {
					clusterName = value
				}
			}
		}
	}

	var count int64
	workflows(h.DB, name, namespace, clusterName, 0, 1000000).Count(&count)
	c.JSON(http.StatusOK, response(http.StatusOK, "", []int64{count}))
}

func (h APIHandler) TotalFailedResources(c *gin.Context) {
	var count int64
	var infra3Resources []models.Infra3Resource
	h.DB.Model(&infra3Resources).Where("current_state = 'failed'").Count(&count)
	c.JSON(http.StatusOK, response(http.StatusOK, "", []int64{count}))
}

func (h APIHandler) dashboardRedirect(c *gin.Context) {
	if h.dashboard == nil {
		c.JSON(http.StatusNotFound, response(http.StatusNotFound, "--dashboard not configured", []any{}))
		return
	}
	if *h.dashboard == "" {
		c.JSON(http.StatusNotFound, response(http.StatusNotFound, "--dashboard not configured", []any{}))
		return
	}
	userToken, _ := userToken(c)
	loginRedirect, _ := c.GetQuery("loginRedirect")
	if loginRedirect != "" {
		loginRedirect = "&loginRedirect=" + loginRedirect
	}

	c.Redirect(http.StatusMovedPermanently, *h.dashboard+"?token="+userToken+loginRedirect)
}
