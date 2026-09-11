package service

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type interleavedPreferencesRepo struct {
	interfaces.UserRepository
	beforeWrite func() error
}

func (r *interleavedPreferencesRepo) UpdateUser(ctx context.Context, user *types.User) error {
	if err := r.beforeWrite(); err != nil {
		return err
	}
	return r.UserRepository.UpdateUser(ctx, user)
}

func (r *interleavedPreferencesRepo) UpdateUserPreferences(
	ctx context.Context, userID string, patch types.UserPreferences,
) (types.UserPreferences, error) {
	if err := r.beforeWrite(); err != nil {
		return types.UserPreferences{}, err
	}
	return r.UserRepository.UpdateUserPreferences(ctx, userID, patch)
}

func TestBrowserPreferencesConcurrentSwitchPreservesCommittedTenant(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "preferences.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&types.User{}, &types.AuthToken{}))
	home, oidc := uint64(7), true
	user := &types.User{ID: "alice", Username: "alice", Email: "alice@example.test", TenantID: home,
		Preferences: types.UserPreferences{LastActiveTenantID: &home, OidcOnlyLogin: &oidc}}
	users := repository.NewUserRepository(db)
	require.NoError(t, users.CreateUser(t.Context(), user))
	tokens := repository.NewAuthTokenRepository(db)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	writeReady, switched := make(chan struct{}), make(chan error, 1)
	go func() {
		select {
		case <-writeReady:
		case <-ctx.Done():
			switched <- ctx.Err()
			return
		}
		access := &types.AuthToken{ID: "access", UserID: user.ID, Token: "access-token", TokenType: "access_token", ExpiresAt: time.Now().Add(time.Hour)}
		refresh := &types.AuthToken{ID: "refresh", UserID: user.ID, Token: "refresh-token", TokenType: "refresh_token", ExpiresAt: time.Now().Add(time.Hour)}
		_, err := tokens.SwitchTenantSession(ctx, user.ID, 42, "", access, refresh)
		switched <- err
	}()
	// 在偏好真正写入前提交另一请求的工作区事务，复现旧代码的过期快照回写。
	repo := &interleavedPreferencesRepo{UserRepository: users, beforeWrite: func() error {
		close(writeReady)
		return <-switched
	}}
	svc := &userService{userRepo: repo}
	custom := "Use my search engine"
	prefs, err := svc.UpdateUserPreferences(ctx, user.ID, types.UserPreferences{BrowserSearchInstructions: &custom})
	require.NoError(t, err)
	stored, err := users.GetUserByID(ctx, user.ID)
	require.NoError(t, err)
	require.NotNil(t, stored.Preferences.LastActiveTenantID)
	require.Equal(t, uint64(42), *stored.Preferences.LastActiveTenantID, "偏好保存不得覆盖已提交的工作区切换")
	require.Equal(t, uint64(42), *prefs.LastActiveTenantID)
	require.Equal(t, custom, stored.Preferences.EffectiveBrowserSearchInstructions())
	require.True(t, *stored.Preferences.OidcOnlyLogin)
	var count int64
	require.NoError(t, db.Model(&types.AuthToken{}).Count(&count).Error)
	require.Equal(t, int64(2), count)
}
