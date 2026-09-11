package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/config"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type browserPreferencesUserService struct {
	interfaces.UserService
	updatedUser string
	patch       types.UserPreferences
}

func (s *browserPreferencesUserService) GetCurrentUser(context.Context) (*types.User, error) {
	return &types.User{ID: "current-user"}, nil
}

func (s *browserPreferencesUserService) BuildLoginMemberships(
	context.Context, *types.User, *types.Tenant,
) []types.Membership {
	return nil
}

func (s *browserPreferencesUserService) UpdateUserPreferences(
	_ context.Context, id string, patch types.UserPreferences,
) (types.UserPreferences, error) {
	s.updatedUser, s.patch = id, patch
	return patch, nil
}

func TestBrowserPreferenceAPIExposesDefaultAndUpdatesCurrentUser(t *testing.T) {
	users := &browserPreferencesUserService{}
	h := &AuthHandler{userService: users, configInfo: &config.Config{Tenant: &config.TenantConfig{}}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/auth/me", nil)
	h.GetCurrentUser(c)
	require.Equal(t, 200, w.Code)
	var payload struct {
		Data struct {
			Defaults map[string]string `json:"preference_defaults"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	require.Equal(t, types.DefaultBrowserSearchInstructions, payload.Data.Defaults["browser_search_instructions"])
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("PUT", "/auth/me/preferences", strings.NewReader(
		`{"user_id":"another-user","oidc_only_login":false,"browser_search_instructions":"Use my engine"}`,
	))
	c.Request.Header.Set("Content-Type", "application/json")
	h.UpdateMyPreferences(c)
	require.Equal(t, 200, w.Code)
	require.Equal(t, "current-user", users.updatedUser)
	require.NotNil(t, users.patch.BrowserSearchInstructions)
	require.Equal(t, "Use my engine", *users.patch.BrowserSearchInstructions)
	require.Nil(t, users.patch.OidcOnlyLogin, "客户端不能修改服务端控制的登录标记")
	users.updatedUser = ""
	c, _ = gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("PUT", "/auth/me/preferences", strings.NewReader(
		`{"browser_search_instructions":"`+strings.Repeat("a", 4001)+`"}`,
	))
	c.Request.Header.Set("Content-Type", "application/json")
	h.UpdateMyPreferences(c)
	require.NotEmpty(t, c.Errors)
	require.Empty(t, users.updatedUser, "invalid input must not reach the preference service")
}

func TestBrowserPreferenceAPIRequiresAuthentication(t *testing.T) {
	h := &AuthHandler{userService: &stubChangePasswordUserService{
		getCurrentUser: func(context.Context) (*types.User, error) {
			return nil, errors.New("未登录")
		},
	}}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPut, "/auth/me/preferences", strings.NewReader(
		`{"browser_search_instructions":"Use my engine"}`,
	))
	h.UpdateMyPreferences(c)
	require.Len(t, c.Errors, 1)
	var appErr *apperrors.AppError
	require.ErrorAs(t, c.Errors.Last().Err, &appErr)
	require.Equal(t, http.StatusUnauthorized, appErr.HTTPCode)
}

func TestBrowserPreferenceAPIAcceptsUnicodeCharacterLimit(t *testing.T) {
	users := &browserPreferencesUserService{}
	h := &AuthHandler{userService: users}
	text := strings.Repeat("中", types.MaxBrowserSearchInstructionsLength)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPut, "/auth/me/preferences", strings.NewReader(
		`{"browser_search_instructions":"`+text+`"}`,
	))
	c.Request.Header.Set("Content-Type", "application/json")
	h.UpdateMyPreferences(c)
	require.Empty(t, c.Errors)
	require.Equal(t, http.StatusOK, w.Code)
	require.NotNil(t, users.patch.BrowserSearchInstructions)
	require.Equal(t, text, *users.patch.BrowserSearchInstructions)
}
