package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/types"
	"golang.org/x/crypto/bcrypt"
)

type emailDomainUserRepo struct {
	adminCreateUserRepo
}

func (r *emailDomainUserRepo) GetUserByEmail(_ context.Context, email string) (*types.User, error) {
	if user := r.existingByEmail; user != nil && user.Email == email {
		return user, nil
	}
	return nil, nil
}

func TestRegisterRequiresSeewayEmailBeforeCreatingResources(t *testing.T) {
	for _, email := range []string{
		"person@example.com", "person@sub.seeway.co", "person@seeway.co.example.com",
		"person@notseeway.co", "@seeway.co", "person@@seeway.co",
		"person@seeway.co ", "Name <person@seeway.co>",
	} {
		t.Run(email, func(t *testing.T) {
			repo := &emailDomainUserRepo{}
			tenants := &provisioningTenantService{}
			svc := &userService{userRepo: repo, tenantService: tenants}
			_, err := svc.Register(context.Background(), &types.RegisterRequest{
				Username: "new-user", Email: email, Password: "SecurePass9",
			})
			if !errors.Is(err, ErrRegistrationEmailDomain) {
				t.Fatalf("注册错误=%v，应拒绝非公司邮箱", err)
			}
			if repo.created != nil || tenants.createCalls != 0 {
				t.Fatal("被拒绝的注册创建了账号或空间")
			}
		})
	}
	for _, email := range []string{"person@seeway.co", "person@SEEWAY.CO", "person+team@seeway.co"} {
		t.Run(email, func(t *testing.T) {
			repo := &emailDomainUserRepo{}
			svc := &userService{userRepo: repo}
			user, err := svc.Register(context.Background(), &types.RegisterRequest{
				Username: "new-user", Email: email, Password: "SecurePass9",
				TenantProvisioning: types.TenantProvisioningTenantless,
			})
			if err != nil || user == nil || repo.created == nil || user.Email != email {
				t.Fatalf("公司邮箱注册失败：user=%v err=%v", user, err)
			}
		})
	}
}

func TestLoginPreservesExistingNonCompanyAccounts(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("ExistingPass9"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	user := &types.User{ID: "existing-user", Email: "existing@example.com", IsActive: true, PasswordHash: string(hash)}
	repo := &emailDomainUserRepo{adminCreateUserRepo: adminCreateUserRepo{existingByEmail: user}}
	svc := &userService{userRepo: repo, tokenRepo: &stubAuthTokenRepo{}}
	for _, test := range []struct {
		password string
		allowed  bool
	}{{"ExistingPass9", true}, {"WrongPass9", false}} {
		response, err := svc.Login(context.Background(), &types.LoginRequest{Email: user.Email, Password: test.password})
		if err != nil || response == nil || response.Success != test.allowed {
			t.Fatalf("旧账号登录结果=%v err=%v，应成功=%v", response, err, test.allowed)
		}
		if test.allowed && (response.Token == "" || response.User.Email != user.Email) {
			t.Fatal("旧账号没有取得有效登录响应")
		}
	}
}

func TestOIDCEmailDomainPolicyPreservesExistingUser(t *testing.T) {
	withOIDCSSRFWhitelist(t, "127.0.0.1")
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/token" {
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "test-access"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"email": "existing@example.com", "name": "existing-user"})
	}))
	defer idp.Close()
	for _, existing := range []bool{false, true} {
		repo := &emailDomainUserRepo{}
		if existing {
			repo.existingByEmail = &types.User{ID: "existing-user", Email: "existing@example.com", IsActive: true}
		}
		svc := &userService{userRepo: repo, tokenRepo: &stubAuthTokenRepo{}, config: &config.Config{
			OIDCAuth: &config.OIDCAuthConfig{
				Enable: true, ClientID: "test-client", AuthorizationEndpoint: idp.URL + "/authorize",
				TokenEndpoint: idp.URL + "/token", UserInfoEndpoint: idp.URL + "/userinfo",
			},
		}}
		response, err := svc.LoginWithOIDC(context.Background(), "test-code", "https://example.com/callback", types.TenantProvisioningTenantless)
		if existing {
			if err != nil || response == nil || !response.Success || response.IsNewUser {
				t.Fatalf("旧账号 OIDC 登录失败：response=%v err=%v", response, err)
			}
		} else if !errors.Is(err, ErrRegistrationEmailDomain) {
			t.Fatalf("OIDC 自动注册绕过公司邮箱限制：err=%v", err)
		}
		if repo.created != nil {
			t.Fatal("OIDC 创建了非公司邮箱账号")
		}
	}
}

func TestAdminCreateUserRejectsNewNonCompanyEmail(t *testing.T) {
	repo := &adminCreateUserRepo{}
	svc := newAdminCreateUserService(repo)
	_, _, err := svc.AdminCreateUser(context.Background(), &types.AdminCreateUserRequest{
		Username: "new-user", Email: "new@example.com", Password: new("SecurePass9"),
	}, types.TenantProvisioningTenantless)
	if !errors.Is(err, ErrRegistrationEmailDomain) || repo.created != nil {
		t.Fatalf("管理员新建账号未执行公司邮箱限制：err=%v", err)
	}
}
