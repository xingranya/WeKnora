package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/repository"
	werrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type tenantDeleteRepositoryStub struct {
	interfaces.TenantRepository
	tenant    *types.Tenant
	getErr    error
	deleteErr error
}

func (s *tenantDeleteRepositoryStub) GetTenantByID(context.Context, uint64) (*types.Tenant, error) {
	return s.tenant, s.getErr
}

func TestDeleteTenant_ReturnsNotFoundWhenTenantIsMissing(t *testing.T) {
	repo := &tenantDeleteRepositoryStub{getErr: repository.ErrTenantNotFound}
	service := NewTenantService(repo, nil)

	err := service.DeleteTenant(context.Background(), 42)
	appErr, ok := werrors.IsAppError(err)
	require.True(t, ok)
	assert.Equal(t, http.StatusNotFound, appErr.HTTPCode)
	assert.Equal(t, werrors.ErrTenantNotFound, appErr.Code)
}

func (s *tenantDeleteRepositoryStub) DeleteTenant(context.Context, uint64) error {
	return s.deleteErr
}

func TestDeleteTenant_ReturnsActionableConflictWhenResourcesRemain(t *testing.T) {
	repo := &tenantDeleteRepositoryStub{
		tenant:    &types.Tenant{ID: 42, Name: "仍在使用的空间"},
		deleteErr: repository.ErrTenantHasKnowledgeBase,
	}
	service := NewTenantService(repo, nil)

	err := service.DeleteTenant(context.Background(), 42)
	appErr, ok := werrors.IsAppError(err)
	require.True(t, ok)
	assert.Equal(t, http.StatusConflict, appErr.HTTPCode)
	assert.Equal(t, werrors.ErrConflict, appErr.Code)
	assert.Equal(t, "空间中仍有知识库或待清理资源，请先删除所有知识库并等待清理完成后重试", appErr.Message)
}
