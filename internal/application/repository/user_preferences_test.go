package repository

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestUserPreferencesPatchPreservesIdentityAndOtherPreferences(t *testing.T) {
	db := newAuthTokenTestDB(t, false)
	require.NoError(t, db.AutoMigrate(&types.User{}))
	repo := NewUserRepository(db)
	oidc, requestedOIDC := true, false
	user := &types.User{ID: "alice", Username: "alice", Email: "alice@example.test", PasswordHash: "password-hash",
		IsActive: true, IsSystemAdmin: true, Preferences: types.UserPreferences{OidcOnlyLogin: &oidc}}
	require.NoError(t, repo.CreateUser(t.Context(), user))
	custom, tenant := "Use my search engine", uint64(42)
	prefs, err := repo.UpdateUserPreferences(t.Context(), user.ID, types.UserPreferences{
		BrowserSearchInstructions: &custom, OidcOnlyLogin: &requestedOIDC,
	})
	require.NoError(t, err)
	require.True(t, *prefs.OidcOnlyLogin, "公开偏好写入不能更改登录来源标记")
	prefs, err = repo.UpdateUserPreferences(t.Context(), user.ID, types.UserPreferences{LastActiveTenantID: &tenant})
	require.NoError(t, err)
	require.Equal(t, custom, prefs.EffectiveBrowserSearchInstructions())
	require.Equal(t, tenant, *prefs.LastActiveTenantID)
	stored, err := repo.GetUserByID(t.Context(), user.ID)
	require.NoError(t, err)
	require.Equal(t, user.PasswordHash, stored.PasswordHash)
	require.Equal(t, user.Username, stored.Username)
	require.True(t, stored.IsSystemAdmin)
	assertNullTenantID(t, db, user.ID)
	blank, zero := "", uint64(0)
	prefs, err = repo.UpdateUserPreferences(t.Context(), user.ID, types.UserPreferences{
		BrowserSearchInstructions: &blank, LastActiveTenantID: &zero,
	})
	require.NoError(t, err)
	require.Nil(t, prefs.BrowserSearchInstructions)
	require.Nil(t, prefs.LastActiveTenantID)
	require.True(t, *prefs.OidcOnlyLogin)
	stored, err = repo.GetUserByID(t.Context(), user.ID)
	require.NoError(t, err)
	require.Equal(t, prefs, stored.Preferences)
}

func TestUserPreferencesPatchFailureLeavesStoredPreferencesUntouched(t *testing.T) {
	db := newAuthTokenTestDB(t, false)
	require.NoError(t, db.AutoMigrate(&types.User{}))
	repo := NewUserRepository(db)
	home, before := uint64(7), "Original search engine"
	user := &types.User{ID: "alice", Username: "alice", Email: "alice@example.test",
		Preferences: types.UserPreferences{LastActiveTenantID: &home, BrowserSearchInstructions: &before}}
	require.NoError(t, repo.CreateUser(t.Context(), user))
	require.NoError(t, db.Exec(`CREATE TRIGGER reject_preferences BEFORE UPDATE OF preferences ON users BEGIN SELECT RAISE(ABORT, 'forced preference failure'); END`).Error)
	custom, tenant := "Replacement search engine", uint64(42)
	prefs, err := repo.UpdateUserPreferences(t.Context(), user.ID, types.UserPreferences{
		BrowserSearchInstructions: &custom, LastActiveTenantID: &tenant,
	})
	require.Error(t, err)
	require.Equal(t, types.UserPreferences{}, prefs, "写入失败不能返回未落库的偏好")
	stored, err := repo.GetUserByID(t.Context(), user.ID)
	require.NoError(t, err)
	require.Equal(t, user.Preferences, stored.Preferences)
	require.WithinDuration(t, user.UpdatedAt, stored.UpdatedAt, 0)
	_, err = repo.UpdateUserPreferences(t.Context(), "missing-user", types.UserPreferences{BrowserSearchInstructions: &custom})
	require.ErrorIs(t, err, ErrUserNotFound)
}
