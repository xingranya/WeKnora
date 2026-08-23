package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestModelRepositoryUpdate_PreservesImmutableColumns(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.Model{}))

	createdAt := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	model := &types.Model{
		ID:          "model-immutable",
		TenantID:    42,
		Name:        "before",
		DisplayName: "修改前",
		Type:        types.ModelTypeKnowledgeQA,
		Source:      types.ModelSourceRemote,
		Parameters:  types.ModelParameters{Provider: "openai"},
		Status:      types.ModelStatusActive,
		CreatedAt:   createdAt,
	}
	require.NoError(t, db.Create(model).Error)

	update := *model
	update.DisplayName = ""
	update.Description = ""
	update.IsDefault = false
	update.CreatedAt = time.Time{}
	update.DeletedAt = gorm.DeletedAt{Time: time.Now(), Valid: true}
	require.NoError(t, NewModelRepository(db).UpdateConfiguration(context.Background(), &update))

	var persisted types.Model
	require.NoError(t, db.Where("id = ?", model.ID).First(&persisted).Error)
	assert.Equal(t, createdAt, persisted.CreatedAt.UTC())
	assert.False(t, persisted.DeletedAt.Valid)
	assert.Empty(t, persisted.DisplayName)
	assert.Empty(t, persisted.Description)
	assert.False(t, persisted.IsDefault)
}

func TestModelRepositoryConcurrentConfigurationAndCredentialsPreserveBothUpdates(t *testing.T) {
	replacementSecret := "secret-new"
	credentialMutations := []struct {
		name          string
		apiKey        string
		appSecret     *string
		wantAPIKey    string
		wantAppSecret string
	}{
		{
			name:          "put_credentials",
			apiKey:        "sk-new",
			appSecret:     &replacementSecret,
			wantAPIKey:    "sk-new",
			wantAppSecret: "secret-new",
		},
		{
			name:          "delete_api_key",
			apiKey:        "",
			appSecret:     nil,
			wantAPIKey:    "",
			wantAppSecret: "secret-old",
		},
	}
	orders := []struct {
		name             string
		credentialsFirst bool
	}{
		{name: "credentials_then_configuration", credentialsFirst: true},
		{name: "configuration_then_credentials", credentialsFirst: false},
	}

	for _, mutation := range credentialMutations {
		for _, order := range orders {
			t.Run(mutation.name+"/"+order.name, func(t *testing.T) {
				db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
				require.NoError(t, err)
				require.NoError(t, db.AutoMigrate(&types.Model{}))

				model := &types.Model{
					ID:          "model-concurrent",
					TenantID:    42,
					Name:        "gpt-original",
					DisplayName: "原显示名",
					Type:        types.ModelTypeKnowledgeQA,
					Source:      types.ModelSourceRemote,
					Parameters: types.ModelParameters{
						BaseURL:   "https://old.example.com/v1",
						Provider:  "openai",
						APIKey:    "sk-old",
						AppSecret: "secret-old",
					},
					Status: types.ModelStatusActive,
				}
				require.NoError(t, db.Create(model).Error)

				configuration := *model
				configuration.DisplayName = "新显示名"
				configuration.Parameters.BaseURL = "https://new.example.com/v1"
				repo := NewModelRepository(db)

				updateConfiguration := func() {
					require.NoError(t, repo.UpdateConfiguration(context.Background(), &configuration))
				}
				updateCredentials := func() {
					require.NoError(t, repo.UpdateCredentials(
						context.Background(),
						model.TenantID,
						model.ID,
						&mutation.apiKey,
						mutation.appSecret,
						nil,
					))
				}
				if order.credentialsFirst {
					updateCredentials()
					updateConfiguration()
				} else {
					updateConfiguration()
					updateCredentials()
				}

				var persisted types.Model
				require.NoError(t, db.Where("id = ?", model.ID).First(&persisted).Error)
				assert.Equal(t, "新显示名", persisted.DisplayName)
				assert.Equal(t, "https://new.example.com/v1", persisted.Parameters.BaseURL)
				assert.Equal(t, mutation.wantAPIKey, persisted.Parameters.APIKey)
				assert.Equal(t, mutation.wantAppSecret, persisted.Parameters.AppSecret)
			})
		}
	}
}

func TestModelRepositoryStatusUpdateDoesNotOverwriteConfigurationOrCredentials(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.Model{}))

	model := &types.Model{
		ID:          "model-status",
		TenantID:    42,
		Name:        "local-model",
		DisplayName: "原显示名",
		Type:        types.ModelTypeKnowledgeQA,
		Source:      types.ModelSourceLocal,
		Parameters: types.ModelParameters{
			Provider:  "ollama",
			APIKey:    "sk-old",
			AppSecret: "secret-old",
		},
		Status: types.ModelStatusDownloading,
	}
	require.NoError(t, db.Create(model).Error)
	repo := NewModelRepository(db)

	configuration := *model
	configuration.DisplayName = "新显示名"
	newAPIKey := "sk-new"
	newAppSecret := "secret-new"
	require.NoError(t, repo.UpdateConfiguration(context.Background(), &configuration))
	require.NoError(t, repo.UpdateCredentials(
		context.Background(),
		model.TenantID,
		model.ID,
		&newAPIKey,
		&newAppSecret,
		nil,
	))
	require.NoError(t, repo.UpdateStatus(
		context.Background(),
		model.TenantID,
		model.ID,
		types.ModelStatusActive,
	))

	var persisted types.Model
	require.NoError(t, db.Where("id = ?", model.ID).First(&persisted).Error)
	assert.Equal(t, types.ModelStatusActive, persisted.Status)
	assert.Equal(t, "新显示名", persisted.DisplayName)
	assert.Equal(t, "sk-new", persisted.Parameters.APIKey)
	assert.Equal(t, "secret-new", persisted.Parameters.AppSecret)
}
