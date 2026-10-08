package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

type desktopEmailUserService struct {
	interfaces.UserService
	user         *types.User
	createdEmail string
}

func (s *desktopEmailUserService) GetUserByEmail(_ context.Context, email string) (*types.User, error) {
	if s.user != nil && s.user.Email == email {
		return s.user, nil
	}
	return nil, nil
}

func (s *desktopEmailUserService) Register(_ context.Context, req *types.RegisterRequest) (*types.User, error) {
	s.createdEmail = req.Email
	s.user = &types.User{ID: "desktop-user", Email: req.Email, TenantID: 42}
	return s.user, nil
}

func (s *desktopEmailUserService) GenerateTokens(context.Context, *types.User) (string, string, error) {
	return "test-access", "test-refresh", nil
}

type desktopEmailTenantService struct{ interfaces.TenantService }

func (*desktopEmailTenantService) GetTenantByID(_ context.Context, id uint64) (*types.Tenant, error) {
	return &types.Tenant{ID: id}, nil
}

func TestAutoSetupPreservesOldEmailAndUsesCompanyEmailForNewUser(t *testing.T) {
	oldEdition := Edition
	Edition = "lite"
	t.Cleanup(func() { Edition = oldEdition })
	gin.SetMode(gin.TestMode)
	for _, existing := range []bool{false, true} {
		users := &desktopEmailUserService{}
		if existing {
			users.user = &types.User{ID: "legacy-desktop-user", Email: "admin@weknora.local", TenantID: 42}
		}
		h := &AuthHandler{userService: users, tenantService: &desktopEmailTenantService{}}
		r := gin.New()
		r.POST("/auth/auto-setup", h.AutoSetup)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/auth/auto-setup", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("桌面版初始化失败：status=%d body=%s", w.Code, w.Body.String())
		}
		if existing && users.createdEmail != "" {
			t.Fatal("旧桌面版账号被重新创建")
		}
		if !existing && users.createdEmail != "admin@seeway.co" {
			t.Fatalf("新桌面版账号邮箱=%q，应符合公司邮箱规则", users.createdEmail)
		}
	}
}
