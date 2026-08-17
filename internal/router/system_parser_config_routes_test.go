package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestPlatformParserEngineConfigRoutesRequireSystemAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, role := range []types.TenantRole{
		types.TenantRoleViewer,
		types.TenantRoleAdmin,
		types.TenantRoleOwner,
	} {
		t.Run(string(role), func(t *testing.T) {
			engine := gin.New()
			engine.Use(func(c *gin.Context) {
				ctx := context.WithValue(c.Request.Context(), types.TenantRoleContextKey, role)
				ctx = context.WithValue(ctx, types.SystemAdminContextKey, false)
				c.Request = c.Request.WithContext(ctx)
				c.Next()
			})

			guards := &rbacGuards{
				cfg:              &config.Config{},
				apiKeyAuthorizer: middleware.NewAPIKeyRouteAuthorizer(),
			}
			RegisterSystemAdminRoutes(engine.Group("/api/v1"), &handler.SystemHandler{}, nil, guards)

			requests := []struct {
				method string
				path   string
				body   string
			}{
				{method: http.MethodGet, path: "/api/v1/system/admin/parser-engine-config"},
				{method: http.MethodPut, path: "/api/v1/system/admin/parser-engine-config", body: `{}`},
				{method: http.MethodPost, path: "/api/v1/system/admin/parser-engine-config/check", body: `{}`},
			}

			for _, request := range requests {
				recorder := httptest.NewRecorder()
				req := httptest.NewRequest(request.method, request.path, strings.NewReader(request.body))
				if request.body != "" {
					req.Header.Set("Content-Type", "application/json")
				}
				engine.ServeHTTP(recorder, req)
				require.Equal(t, http.StatusForbidden, recorder.Code)
			}
		})
	}
}
