package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateSystemAESKeyForStartup(t *testing.T) {
	tests := []struct {
		name    string
		edition string
		ginMode string
		key     string
		wantErr bool
	}{
		{name: "生产缺失", edition: "standard", ginMode: "release", wantErr: true},
		{name: "生产长度错误", edition: "standard", ginMode: "release", key: strings.Repeat("k", 31), wantErr: true},
		{name: "生产有效", edition: "standard", ginMode: "release", key: "0123456789abcdefghijklmnopqrstuv"},
		{name: "生产拒绝历史默认值", edition: "standard", ginMode: "release", key: legacyDefaultSystemAESKey, wantErr: true},
		{name: "生产拒绝单字符填充", edition: "standard", ginMode: "release", key: strings.Repeat("k", 32), wantErr: true},
		{name: "生产拒绝短模式重复", edition: "standard", ginMode: "release", key: strings.Repeat("abcd1234", 4), wantErr: true},
		{name: "生产拒绝占位词", edition: "standard", ginMode: "release", key: "change-me-now-0123456789abcdefghi", wantErr: true},
		{name: "开发兼容", edition: "standard", ginMode: "debug"},
		{name: "测试兼容", edition: "standard", ginMode: "test", key: "short"},
		{name: "Lite 兼容", edition: "lite", ginMode: "release"},
		{name: "未知模式拒绝", edition: "standard", ginMode: "prod", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSystemAESKeyForStartup(tt.edition, tt.ginMode, tt.key)
			if tt.wantErr {
				require.Error(t, err)
				if tt.key != "" {
					require.NotContains(t, err.Error(), tt.key)
				}
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestValidateProductionStartupConfig(t *testing.T) {
	validAES := "0123456789abcdefghijklmnopqrstuv"
	validJWT := "jwt.0123456789abcdefghijklmnopqrstuvwxyz.ABCDEFGH"

	mode, err := ValidateProductionStartupConfig(
		"standard", " Release ", validAES, validJWT, "false", "false",
	)
	require.NoError(t, err)
	require.Equal(t, "release", mode)

	tests := []struct {
		name        string
		aes         string
		jwt         string
		autoMigrate string
		autoRecover string
	}{
		{name: "缺少 AES", jwt: validJWT, autoMigrate: "false", autoRecover: "false"},
		{name: "JWT 太短", aes: validAES, jwt: "short", autoMigrate: "false", autoRecover: "false"},
		{name: "JWT 示例值", aes: validAES, jwt: legacyDefaultJWTSecret, autoMigrate: "false", autoRecover: "false"},
		{name: "JWT 低字符多样性", aes: validAES, jwt: strings.Repeat("j", 48), autoMigrate: "false", autoRecover: "false"},
		{name: "JWT 短模式重复", aes: validAES, jwt: strings.Repeat("token123", 6), autoMigrate: "false", autoRecover: "false"},
		{name: "自动迁移未关闭", aes: validAES, jwt: validJWT, autoMigrate: "true", autoRecover: "false"},
		{name: "自动迁移大写 false 被拒绝", aes: validAES, jwt: validJWT, autoMigrate: "FALSE", autoRecover: "false"},
		{name: "自动迁移带空格被拒绝", aes: validAES, jwt: validJWT, autoMigrate: " false ", autoRecover: "false"},
		{name: "dirty 自动恢复未关闭", aes: validAES, jwt: validJWT, autoMigrate: "false", autoRecover: "true"},
		{name: "dirty 自动恢复大写 false 被拒绝", aes: validAES, jwt: validJWT, autoMigrate: "false", autoRecover: "FALSE"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ValidateProductionStartupConfig(
				"standard", "release", tt.aes, tt.jwt, tt.autoMigrate, tt.autoRecover,
			)
			require.Error(t, err)
			if tt.aes != "" {
				require.NotContains(t, err.Error(), tt.aes)
			}
			if tt.jwt != "" {
				require.NotContains(t, err.Error(), tt.jwt)
			}
		})
	}

	mode, err = ValidateProductionStartupConfig("standard", "debug", "", "", "", "")
	require.NoError(t, err)
	require.Equal(t, "debug", mode)
	mode, err = ValidateProductionStartupConfig("lite", "release", "", "", "", "")
	require.NoError(t, err)
	require.Equal(t, "release", mode)
}

func TestValidateProductionMigrationState(t *testing.T) {
	require.NoError(t, ValidateProductionMigrationState(
		"standard", "release", "postgres", MinimumProductionMigrationVersion, false, true,
	))
	require.Error(t, ValidateProductionMigrationState(
		"standard", "release", "postgres", MinimumProductionMigrationVersion-1, false, true,
	))
	require.Error(t, ValidateProductionMigrationState(
		"standard", "release", "postgres", MinimumProductionMigrationVersion+1, false, true,
	))
	require.Error(t, ValidateProductionMigrationState(
		"standard", "release", "postgres", MinimumProductionMigrationVersion, true, true,
	))
	require.Error(t, ValidateProductionMigrationState(
		"standard", "release", "postgres", 0, false, false,
	))
	require.NoError(t, ValidateProductionMigrationState(
		"standard", "debug", "postgres", 0, true, false,
	))
	require.NoError(t, ValidateProductionMigrationState(
		"standard", "release", "sqlite", 0, true, false,
	))
}
