package repository

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newAuthTokenTestDB(t *testing.T, withFingerprint bool) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.AuthToken{}))
	if withFingerprint {
		require.NoError(t, db.Exec(
			"ALTER TABLE auth_tokens ADD COLUMN token_fingerprint VARCHAR(64)",
		).Error)
		require.NoError(t, db.Exec(
			"CREATE UNIQUE INDEX idx_auth_tokens_token_fingerprint ON auth_tokens(token_fingerprint) WHERE token_fingerprint IS NOT NULL AND is_revoked = 0",
		).Error)
	}
	return db
}

func TestAuthTokenRepositoryCreatePairRollsBackOnSecondWriteFailure(t *testing.T) {
	db := newAuthTokenTestDB(t, true)
	repo := NewAuthTokenRepository(db)
	now := time.Now().UTC()
	access := &types.AuthToken{
		ID: "duplicate-id", UserID: "user-1", Token: "access", TokenType: "access_token",
		ExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
	}
	refresh := &types.AuthToken{
		ID: "duplicate-id", UserID: "user-1", Token: "refresh", TokenType: "refresh_token",
		ExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
	}
	require.Error(t, repo.CreateTokenPair(context.Background(), access, refresh))
	var count int64
	require.NoError(t, db.Model(&types.AuthToken{}).Count(&count).Error)
	require.Zero(t, count, "token pair 必须全部提交或全部回滚")
}

func TestAuthTokenRepositoryRotateRevokesAllLegacyDuplicatesOnce(t *testing.T) {
	db := newAuthTokenTestDB(t, true)
	repo := NewAuthTokenRepository(db)
	now := time.Now().UTC()
	for _, id := range []string{"legacy-a", "legacy-b"} {
		require.NoError(t, db.Exec(`
			INSERT INTO auth_tokens (id, user_id, token, token_type, expires_at, is_revoked, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`, id, "user-1", "same-refresh-token", "refresh_token", now.Add(time.Hour), false, now, now).Error)
	}
	access := &types.AuthToken{
		ID: "new-access", UserID: "user-1", Token: "new-access-token", TokenType: "access_token",
		ExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
	}
	refresh := &types.AuthToken{
		ID: "new-refresh", UserID: "user-1", Token: "new-refresh-token", TokenType: "refresh_token",
		ExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, repo.RotateRefreshToken(
		context.Background(), "same-refresh-token", "user-1", access, refresh,
	))
	var activeLegacy int64
	require.NoError(t, db.Model(&types.AuthToken{}).
		Where("token = ? AND is_revoked = ?", "same-refresh-token", false).
		Count(&activeLegacy).Error)
	require.Zero(t, activeLegacy)

	secondAccess := *access
	secondAccess.ID = "second-access"
	secondAccess.Token = "second-access-token"
	secondRefresh := *refresh
	secondRefresh.ID = "second-refresh"
	secondRefresh.Token = "second-refresh-token"
	require.ErrorIs(t, repo.RotateRefreshToken(
		context.Background(), "same-refresh-token", "user-1", &secondAccess, &secondRefresh,
	), ErrTokenNotFound)
}

func TestAuthTokenRepositoryDualWritesAndQueriesFingerprint(t *testing.T) {
	db := newAuthTokenTestDB(t, true)
	repo := NewAuthTokenRepository(db)
	ctx := context.Background()
	rawToken := "header.payload.signature-access-secret"
	token := &types.AuthToken{
		ID: "token-1", UserID: "user-1", Token: rawToken, TokenType: "access_token",
		ExpiresAt: time.Now().Add(time.Hour),
	}

	require.NoError(t, repo.CreateToken(ctx, token))
	require.Equal(t, rawToken, token.Token)

	var stored struct {
		Token            string
		TokenFingerprint string
	}
	require.NoError(t, db.Table("auth_tokens").
		Select("token, token_fingerprint").
		Where("id = ?", token.ID).
		Scan(&stored).Error)
	require.Equal(t, rawToken, stored.Token, "旧镜像必须仍能按明文 token 查询")
	require.Equal(t, authTokenFingerprint(rawToken), stored.TokenFingerprint)

	loaded, err := repo.GetTokenByValue(ctx, rawToken)
	require.NoError(t, err)
	require.Equal(t, token.ID, loaded.ID)
	require.Equal(t, rawToken, loaded.Token)
}

func TestAuthTokenRepositoryBackfillsFingerprintWithoutDestroyingPlaintext(t *testing.T) {
	db := newAuthTokenTestDB(t, true)
	ctx := context.Background()
	rawToken := "legacy.refresh.token"

	require.NoError(t, db.Exec(`
		INSERT INTO auth_tokens (id, user_id, token, token_type, expires_at, is_revoked, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, "legacy-1", "user-1", rawToken, "refresh_token", time.Now().Add(time.Hour), false, time.Now(), time.Now()).Error)
	repo := NewAuthTokenRepository(db)

	loaded, err := repo.GetTokenByValue(ctx, rawToken)
	require.NoError(t, err)
	require.Equal(t, "legacy-1", loaded.ID)
	require.Equal(t, rawToken, loaded.Token)

	var stored struct {
		Token            string
		TokenFingerprint string
	}
	require.NoError(t, db.Table("auth_tokens").
		Select("token, token_fingerprint").
		Where("id = ?", loaded.ID).
		Scan(&stored).Error)
	require.Equal(t, rawToken, stored.Token)
	require.Equal(t, authTokenFingerprint(rawToken), stored.TokenFingerprint)
}

func TestAuthTokenRepositoryWorksWithoutFingerprintColumn(t *testing.T) {
	db := newAuthTokenTestDB(t, false)
	repo := NewAuthTokenRepository(db)
	ctx := context.Background()
	rawToken := "sqlite-or-rolled-back-token"
	token := &types.AuthToken{
		ID: "legacy-schema-1", UserID: "user-1", Token: rawToken, TokenType: "access_token",
		ExpiresAt: time.Now().Add(time.Hour),
	}

	require.NoError(t, repo.CreateToken(ctx, token))
	loaded, err := repo.GetTokenByValue(ctx, rawToken)
	require.NoError(t, err)
	require.Equal(t, rawToken, loaded.Token)
}

func TestAuthTokenJSONNeverSerializesRawToken(t *testing.T) {
	token := &types.AuthToken{ID: "token-1", UserID: "user-1", Token: strings.Repeat("a", 64)}
	raw, err := json.Marshal(token)
	require.NoError(t, err)
	require.NotContains(t, string(raw), `"token"`)
	require.NotContains(t, string(raw), token.Token)
}

func TestSwitchTenantSessionCommitsPreferenceAndTokensAtomically(t *testing.T) {
	for _, scenario := range []string{"成功", "无效刷新令牌", "第二令牌写入失败", "偏好写入失败"} {
		t.Run(scenario, func(t *testing.T) {
			db := newAuthTokenTestDB(t, true)
			require.NoError(t, db.AutoMigrate(&types.User{}))
			home := uint64(7)
			oidc := true
			user := &types.User{ID: "alice", Username: "alice", Email: "alice@example.test",
				Preferences: types.UserPreferences{LastActiveTenantID: &home, OidcOnlyLogin: &oidc}}
			require.NoError(t, db.Create(user).Error)
			repo := NewAuthTokenRepository(db)
			ctx := context.Background()
			old := &types.AuthToken{ID: "old", UserID: user.ID, Token: "old-refresh", TokenType: "refresh_token", ExpiresAt: time.Now().Add(time.Hour)}
			require.NoError(t, repo.CreateToken(ctx, old))
			access := &types.AuthToken{ID: "new-access", UserID: user.ID, Token: "new-access-token", TokenType: "access_token", ExpiresAt: time.Now().Add(time.Hour)}
			refresh := &types.AuthToken{ID: "new-refresh", UserID: user.ID, Token: "new-refresh-token", TokenType: "refresh_token", ExpiresAt: time.Now().Add(time.Hour)}
			presented := old.Token
			switch scenario {
			case "无效刷新令牌":
				presented = "invalid-refresh"
			case "第二令牌写入失败":
				refresh.ID = access.ID
			case "偏好写入失败":
				require.NoError(t, db.Exec(`CREATE TRIGGER reject_preferences BEFORE UPDATE OF preferences ON users BEGIN SELECT RAISE(ABORT, 'forced preference failure'); END`).Error)
			}
			preferences, err := repo.SwitchTenantSession(ctx, user.ID, 42, presented, access, refresh)
			var storedUser types.User
			require.NoError(t, db.First(&storedUser, "id = ?", user.ID).Error)
			var storedOld types.AuthToken
			require.NoError(t, db.First(&storedOld, "id = ?", old.ID).Error)
			var count int64
			require.NoError(t, db.Model(&types.AuthToken{}).Count(&count).Error)
			require.True(t, *storedUser.Preferences.OidcOnlyLogin)
			if scenario == "成功" {
				require.NoError(t, err)
				require.Equal(t, uint64(42), *preferences.LastActiveTenantID)
				require.Equal(t, uint64(42), *storedUser.Preferences.LastActiveTenantID)
				require.True(t, storedOld.IsRevoked)
				require.Equal(t, int64(3), count)
			} else {
				require.Error(t, err)
				require.Nil(t, preferences.LastActiveTenantID)
				require.Equal(t, home, *storedUser.Preferences.LastActiveTenantID)
				require.False(t, storedOld.IsRevoked)
				require.Equal(t, int64(1), count, "失败切换不得留下新 token")
			}
		})
	}
}
