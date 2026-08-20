package handler

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestTenantAPIKeyListAndUpdateResponsesMaskToken(t *testing.T) {
	rawToken := "sk-tenant-response-secret-1234567890"
	key := &types.TenantAPIKey{
		ID: 7, ScopeType: types.APIKeyScopeTenant, Name: "integration", APIKey: rawToken,
		FullAccess: true, CreatedAt: time.Now().UTC(),
	}
	response := tenantAPIKeyForResponse(key)
	require.Equal(t, maskManagedAPIKey(rawToken), response.APIKey)

	for _, payload := range []any{[]tenantAPIKeyResponse{response}, response} {
		body, err := json.Marshal(payload)
		require.NoError(t, err)
		require.NotContains(t, string(body), rawToken)
		require.Contains(t, string(body), maskManagedAPIKey(rawToken))
	}
}

func TestTenantAPIKeyCreateResponseReturnsFullTokenOnce(t *testing.T) {
	rawToken := "sk-tenant-create-secret-1234567890"
	key := &types.TenantAPIKey{
		ID: 8, ScopeType: types.APIKeyScopeTenant, Name: "created", APIKey: rawToken,
		CreatedAt: time.Now().UTC(),
	}
	payload := tenantAPIKeyCreateResponse{
		tenantAPIKeyResponse: tenantAPIKeyForResponse(key),
		Token:                rawToken,
	}
	body, err := json.Marshal(payload)
	require.NoError(t, err)

	serialized := string(body)
	require.Equal(t, 1, strings.Count(serialized, rawToken))
	require.Contains(t, serialized, `"token":"`+rawToken+`"`)
	require.Contains(t, serialized, `"api_key":"`+maskManagedAPIKey(rawToken)+`"`)
}

func TestTenantAPIKeyEntityNeverSerializesSecret(t *testing.T) {
	rawToken := "sk-entity-secret"
	body, err := json.Marshal(&types.TenantAPIKey{ID: 9, APIKey: rawToken})
	require.NoError(t, err)
	require.NotContains(t, string(body), rawToken)
	require.NotContains(t, string(body), `"api_key"`)
}
