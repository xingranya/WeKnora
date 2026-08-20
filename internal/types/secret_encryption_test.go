package types

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSecretValueAndHooksPropagateEncryptionFailure(t *testing.T) {
	t.Setenv("SYSTEM_AES_KEY", strings.Repeat("k", 32))
	forcedErr := errors.New("forced encryption failure")
	originalEncrypt := encryptAESGCMForStorage
	encryptAESGCMForStorage = func(string, []byte) (string, error) {
		return "", forcedErr
	}
	t.Cleanup(func() { encryptAESGCMForStorage = originalEncrypt })

	tests := []struct {
		name string
		run  func() error
	}{
		{name: "模型 API Key", run: func() error {
			_, err := (ModelParameters{APIKey: "secret"}).Value()
			return err
		}},
		{name: "MCP API Key", run: func() error {
			_, err := (&MCPAuthConfig{APIKey: "secret"}).Value()
			return err
		}},
		{name: "存储凭据", run: func() error {
			_, err := (StorageBackendConfig{SecretAccessKey: "secret"}).Value()
			return err
		}},
		{name: "向量库凭据", run: func() error {
			_, err := (ConnectionConfig{Password: "secret"}).Value()
			return err
		}},
		{name: "联网搜索 API Key", run: func() error {
			_, err := (WebSearchProviderParameters{APIKey: "secret"}).Value()
			return err
		}},
		{name: "API 身份映射密钥", run: func() error {
			_, err := (&APIPrincipalConfig{HMACSecret: "secret"}).Value()
			return err
		}},
		{name: "WeKnoraCloud 密钥", run: func() error {
			_, err := (&CredentialsConfig{WeKnoraCloud: &WeKnoraCloudCredentials{AppSecret: "secret"}}).Value()
			return err
		}},
		{name: "沙箱环境变量", run: func() error {
			_, err := (&TenantSandboxConfig{EnvVars: map[string]string{"TOKEN": "secret"}}).Value()
			return err
		}},
		{name: "数据源凭据", run: func() error {
			_, err := (&DataSourceConfig{Credentials: map[string]interface{}{"token": "secret"}}).ToJSON()
			return err
		}},
		{name: "租户 API Key Hook", run: func() error {
			return (&TenantAPIKey{APIKey: "secret"}).BeforeSave(nil)
		}},
		{name: "MCP OAuth 客户端 Hook", run: func() error {
			return (&MCPOAuthClient{ClientSecret: "secret"}).BeforeSave(nil)
		}},
		{name: "MCP OAuth Token Hook", run: func() error {
			return (&MCPOAuthToken{AccessToken: "secret"}).BeforeSave(nil)
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.ErrorIs(t, tt.run(), forcedErr)
		})
	}
}

func TestSecretValueWithoutKeyKeepsDevelopmentCompatibility(t *testing.T) {
	t.Setenv("SYSTEM_AES_KEY", "")
	value, err := (ModelParameters{APIKey: "development-secret"}).Value()
	require.NoError(t, err)
	require.Contains(t, string(value.([]byte)), "development-secret")
}
