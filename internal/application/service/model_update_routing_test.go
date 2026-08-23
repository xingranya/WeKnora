package service

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModelServiceRoutesConfigurationUpdateToScopedRepositoryMethod(t *testing.T) {
	legacyCalls := 0
	configurationCalls := 0
	repo := &stubModelRepoForDelete{
		model: &types.Model{
			ID:       "model-1",
			TenantID: 42,
			Status:   types.ModelStatusActive,
		},
		update: func(*types.Model) error {
			legacyCalls++
			return nil
		},
		updateConfiguration: func(*types.Model) error {
			configurationCalls++
			return nil
		},
	}
	service := NewModelService(repo, nil, nil, nil, nil, nil)
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(42))

	require.NoError(t, service.UpdateModel(ctx, &types.Model{
		ID:       "model-1",
		TenantID: 42,
		Name:     "updated",
	}))
	assert.Equal(t, 1, configurationCalls)
	assert.Zero(t, legacyCalls)
}

func TestModelServiceRoutesCredentialMutationsToScopedRepositoryMethod(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T, service interfaces.ModelService, ctx context.Context)
	}{
		{
			name: "update",
			run: func(t *testing.T, service interfaces.ModelService, ctx context.Context) {
				t.Helper()
				newKey := "sk-new"
				_, err := service.UpdateModelCredentials(ctx, "model-1", &newKey, nil)
				require.NoError(t, err)
			},
		},
		{
			name: "clear",
			run: func(t *testing.T, service interfaces.ModelService, ctx context.Context) {
				t.Helper()
				require.NoError(t, service.ClearModelCredential(ctx, "model-1", "api_key"))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			legacyCalls := 0
			credentialCalls := 0
			repo := &stubModelRepoForDelete{
				model: &types.Model{
					ID:       "model-1",
					TenantID: 42,
					Status:   types.ModelStatusActive,
					Parameters: types.ModelParameters{
						APIKey: "sk-old",
					},
				},
				update: func(*types.Model) error {
					legacyCalls++
					return nil
				},
				updateCredentials: func(uint64, string, *string, *string, *string) error {
					credentialCalls++
					return nil
				},
			}
			service := NewModelService(repo, nil, nil, nil, nil, nil)
			ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(42))

			tt.run(t, service, ctx)
			assert.Equal(t, 1, credentialCalls)
			assert.Zero(t, legacyCalls)
		})
	}
}
