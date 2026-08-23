package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	werrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type tenantDeleteServiceStub struct {
	interfaces.TenantService
	err error
}

func (s *tenantDeleteServiceStub) DeleteTenant(context.Context, uint64) error {
	return s.err
}

func TestTenantHandlerDeleteTenant_PreservesConflictError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodDelete, "/api/v1/tenants/42", nil)
	c.Params = gin.Params{{Key: "id", Value: "42"}}

	handler := NewTenantHandler(
		&tenantDeleteServiceStub{err: werrors.NewConflictError("请先删除知识库并等待清理完成")},
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
	)
	handler.DeleteTenant(c)

	require.Len(t, c.Errors, 1)
	appErr, ok := werrors.IsAppError(c.Errors[0].Err)
	require.True(t, ok)
	assert.Equal(t, http.StatusConflict, appErr.HTTPCode)
	assert.Equal(t, werrors.ErrConflict, appErr.Code)
	assert.Equal(t, "请先删除知识库并等待清理完成", appErr.Message)
}
