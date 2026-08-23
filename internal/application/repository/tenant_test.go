package repository

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupTestDB creates an in-memory SQLite database with tenant table.
func setupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&types.Tenant{},
		&types.TenantMember{},
		&types.KnowledgeBase{},
		&types.Knowledge{},
		&types.KnowledgeFolder{},
		&types.KnowledgeBaseShare{},
		&types.DataSource{},
		&types.SyncLog{},
		&types.TaskPendingOp{},
	))
	return db
}

func TestDeleteTenant_RejectsActiveKnowledgeBaseWithoutDeletingMemberships(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	repo := NewTenantRepository(db)

	tenant := &types.Tenant{Name: "in-use", Status: "active"}
	require.NoError(t, db.Create(tenant).Error)
	member := &types.TenantMember{
		UserID:   "owner-1",
		TenantID: tenant.ID,
		Role:     types.TenantRoleOwner,
		Status:   types.TenantMemberStatusActive,
	}
	require.NoError(t, db.Create(member).Error)
	require.NoError(t, db.Create(&types.KnowledgeBase{
		ID:       "kb-active",
		Name:     "仍在使用的知识库",
		TenantID: tenant.ID,
	}).Error)

	err := repo.DeleteTenant(ctx, tenant.ID)
	require.ErrorIs(t, err, ErrTenantHasKnowledgeBase)

	var tenantCount int64
	require.NoError(t, db.Model(&types.Tenant{}).Where("id = ?", tenant.ID).Count(&tenantCount).Error)
	assert.Equal(t, int64(1), tenantCount)

	var memberCount int64
	require.NoError(t, db.Model(&types.TenantMember{}).
		Where("tenant_id = ?", tenant.ID).
		Count(&memberCount).Error)
	assert.Equal(t, int64(1), memberCount)
}

func TestDeleteTenant_RejectsDeletedKnowledgeBaseWithPendingResources(t *testing.T) {
	tests := []struct {
		name string
		seed func(t *testing.T, db *gorm.DB, tenantID uint64, knowledgeBaseID string)
	}{
		{
			name: "knowledge",
			seed: func(t *testing.T, db *gorm.DB, tenantID uint64, knowledgeBaseID string) {
				t.Helper()
				require.NoError(t, db.Create(&types.Knowledge{
					ID:              "knowledge-pending",
					TenantID:        tenantID,
					KnowledgeBaseID: knowledgeBaseID,
					Title:           "待清理文档",
				}).Error)
			},
		},
		{
			name: "folder",
			seed: func(t *testing.T, db *gorm.DB, tenantID uint64, knowledgeBaseID string) {
				t.Helper()
				require.NoError(t, db.Create(&types.KnowledgeFolder{
					ID:              "folder-pending",
					TenantID:        tenantID,
					KnowledgeBaseID: knowledgeBaseID,
					Path:            "待清理目录",
				}).Error)
			},
		},
		{
			name: "share",
			seed: func(t *testing.T, db *gorm.DB, tenantID uint64, knowledgeBaseID string) {
				t.Helper()
				require.NoError(t, db.Create(&types.KnowledgeBaseShare{
					ID:              "share-pending",
					KnowledgeBaseID: knowledgeBaseID,
					OrganizationID:  "org-pending",
					SharedByUserID:  "owner-1",
					SourceTenantID:  tenantID,
				}).Error)
			},
		},
		{
			name: "data_source",
			seed: func(t *testing.T, db *gorm.DB, tenantID uint64, knowledgeBaseID string) {
				t.Helper()
				require.NoError(t, db.Create(&types.DataSource{
					ID:              "source-pending",
					TenantID:        tenantID,
					KnowledgeBaseID: knowledgeBaseID,
					Name:            "待清理数据源",
				}).Error)
			},
		},
		{
			name: "pending_operation",
			seed: func(t *testing.T, db *gorm.DB, tenantID uint64, knowledgeBaseID string) {
				t.Helper()
				require.NoError(t, db.Exec(`
					INSERT INTO task_pending_ops
						(tenant_id, task_type, scope, scope_id, op, payload, enqueued_at)
					VALUES (?, 'wiki:ingest', 'knowledge_base', ?, 'ingest', '{}', CURRENT_TIMESTAMP)
				`, tenantID, knowledgeBaseID).Error)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := setupTestDB(t)
			ctx := context.Background()
			repo := NewTenantRepository(db)

			tenant := &types.Tenant{Name: "cleanup-pending", Status: "active"}
			require.NoError(t, db.Create(tenant).Error)
			member := &types.TenantMember{
				UserID:   "owner-1",
				TenantID: tenant.ID,
				Role:     types.TenantRoleOwner,
				Status:   types.TenantMemberStatusActive,
			}
			require.NoError(t, db.Create(member).Error)

			knowledgeBase := &types.KnowledgeBase{
				ID:       "kb-deleted",
				Name:     "已删除知识库",
				TenantID: tenant.ID,
			}
			require.NoError(t, db.Create(knowledgeBase).Error)
			require.NoError(t, db.Delete(knowledgeBase).Error)
			tt.seed(t, db, tenant.ID, knowledgeBase.ID)

			err := repo.DeleteTenant(ctx, tenant.ID)
			require.ErrorIs(t, err, ErrTenantHasKnowledgeBase)

			var memberCount int64
			require.NoError(t, db.Model(&types.TenantMember{}).
				Where("tenant_id = ?", tenant.ID).
				Count(&memberCount).Error)
			assert.Equal(t, int64(1), memberCount)
		})
	}
}

func TestDeleteTenant_RejectsDeletedDataSourceWithNonTerminalSyncLog(t *testing.T) {
	for _, status := range []string{"running", "pending"} {
		t.Run(status, func(t *testing.T) {
			db := setupTestDB(t)
			ctx := context.Background()
			repo := NewTenantRepository(db)

			tenant := &types.Tenant{Name: "sync-cleanup-pending", Status: "active"}
			require.NoError(t, db.Create(tenant).Error)
			member := &types.TenantMember{
				UserID:   "owner-1",
				TenantID: tenant.ID,
				Role:     types.TenantRoleOwner,
				Status:   types.TenantMemberStatusActive,
			}
			require.NoError(t, db.Create(member).Error)

			knowledgeBase := &types.KnowledgeBase{
				ID:       "kb-deleted-syncing",
				Name:     "已删除但仍在同步的知识库",
				TenantID: tenant.ID,
			}
			require.NoError(t, db.Create(knowledgeBase).Error)
			require.NoError(t, db.Delete(knowledgeBase).Error)

			dataSource := &types.DataSource{
				ID:              "source-deleted-syncing",
				TenantID:        tenant.ID,
				KnowledgeBaseID: knowledgeBase.ID,
				Name:            "已删除数据源",
			}
			require.NoError(t, db.Create(dataSource).Error)
			require.NoError(t, db.Delete(dataSource).Error)
			require.NoError(t, db.Exec(`
				INSERT INTO sync_logs
					(id, data_source_id, tenant_id, status, started_at, created_at, updated_at)
				VALUES ('sync-log-pending', ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
			`, dataSource.ID, tenant.ID, status).Error)

			err := repo.DeleteTenant(ctx, tenant.ID)
			require.ErrorIs(t, err, ErrTenantHasKnowledgeBase)

			var memberCount int64
			require.NoError(t, db.Model(&types.TenantMember{}).
				Where("tenant_id = ?", tenant.ID).
				Count(&memberCount).Error)
			assert.Equal(t, int64(1), memberCount)
		})
	}
}

func TestDeleteTenant_AllowsDeletedDataSourceWithTerminalSyncLogs(t *testing.T) {
	for _, status := range []string{
		types.SyncLogStatusSuccess,
		types.SyncLogStatusPartial,
		types.SyncLogStatusFailed,
		types.SyncLogStatusCanceled,
	} {
		t.Run(status, func(t *testing.T) {
			db := setupTestDB(t)
			ctx := context.Background()
			repo := NewTenantRepository(db)

			tenant := &types.Tenant{Name: "sync-cleanup-complete", Status: "active"}
			require.NoError(t, db.Create(tenant).Error)
			knowledgeBase := &types.KnowledgeBase{
				ID:       "kb-deleted-synced",
				Name:     "同步清理已结束的知识库",
				TenantID: tenant.ID,
			}
			require.NoError(t, db.Create(knowledgeBase).Error)
			require.NoError(t, db.Delete(knowledgeBase).Error)

			dataSource := &types.DataSource{
				ID:              "source-deleted-synced",
				TenantID:        tenant.ID,
				KnowledgeBaseID: knowledgeBase.ID,
				Name:            "同步已结束的数据源",
			}
			require.NoError(t, db.Create(dataSource).Error)
			require.NoError(t, db.Delete(dataSource).Error)
			require.NoError(t, db.Exec(`
				INSERT INTO sync_logs
					(id, data_source_id, tenant_id, status, started_at, finished_at, created_at, updated_at)
				VALUES ('sync-log-terminal', ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
			`, dataSource.ID, tenant.ID, status).Error)

			require.NoError(t, repo.DeleteTenant(ctx, tenant.ID))

			var tenantCount int64
			require.NoError(t, db.Model(&types.Tenant{}).
				Where("id = ?", tenant.ID).
				Count(&tenantCount).Error)
			assert.Zero(t, tenantCount)
		})
	}
}

func TestDeleteTenant_AllowsCompletedDeletedKnowledgeBase(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	repo := NewTenantRepository(db)

	tenant := &types.Tenant{Name: "cleanup-complete", Status: "active"}
	require.NoError(t, db.Create(tenant).Error)
	member := &types.TenantMember{
		UserID:   "owner-1",
		TenantID: tenant.ID,
		Role:     types.TenantRoleOwner,
		Status:   types.TenantMemberStatusActive,
	}
	require.NoError(t, db.Create(member).Error)
	knowledgeBase := &types.KnowledgeBase{
		ID:       "kb-cleaned",
		Name:     "已完成清理的知识库",
		TenantID: tenant.ID,
	}
	require.NoError(t, db.Create(knowledgeBase).Error)
	require.NoError(t, db.Delete(knowledgeBase).Error)

	require.NoError(t, repo.DeleteTenant(ctx, tenant.ID))

	var tenantCount int64
	require.NoError(t, db.Model(&types.Tenant{}).Where("id = ?", tenant.ID).Count(&tenantCount).Error)
	assert.Zero(t, tenantCount)
	var memberCount int64
	require.NoError(t, db.Model(&types.TenantMember{}).
		Where("tenant_id = ?", tenant.ID).
		Count(&memberCount).Error)
	assert.Zero(t, memberCount)
}

func TestDeleteTenant_SoftDeletesMemberships(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	repo := NewTenantRepository(db)

	tenant := &types.Tenant{Name: "gone", Status: "active"}
	require.NoError(t, db.Create(tenant).Error)

	member := &types.TenantMember{
		UserID:   "user-1",
		TenantID: tenant.ID,
		Role:     types.TenantRoleOwner,
		Status:   types.TenantMemberStatusActive,
	}
	require.NoError(t, db.Create(member).Error)

	require.NoError(t, repo.DeleteTenant(ctx, tenant.ID))

	var tenantCount int64
	require.NoError(t, db.Model(&types.Tenant{}).Count(&tenantCount).Error)
	assert.Equal(t, int64(0), tenantCount)

	var memberCount int64
	require.NoError(t, db.Model(&types.TenantMember{}).Count(&memberCount).Error)
	assert.Equal(t, int64(0), memberCount)

	// Unscoped: rows still exist but are soft-deleted.
	var rawTenantCount int64
	require.NoError(t, db.Unscoped().Model(&types.Tenant{}).Count(&rawTenantCount).Error)
	assert.Equal(t, int64(1), rawTenantCount)

	var rawMemberCount int64
	require.NoError(t, db.Unscoped().Model(&types.TenantMember{}).Count(&rawMemberCount).Error)
	assert.Equal(t, int64(1), rawMemberCount)
}
