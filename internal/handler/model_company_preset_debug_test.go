package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type companyPresetDebugModelService struct {
	interfaces.ModelService
}

func (s *companyPresetDebugModelService) GetModelByID(context.Context, string) (*types.Model, error) {
	return &types.Model{ID: "company-model", IsBuiltin: true}, nil
}

func TestDebugCompanyPresetModelRequiresSystemAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &ModelHandler{service: &companyPresetDebugModelService{}}
	engine := gin.New()
	engine.Use(middleware.ErrorHandler())
	engine.POST("/models/:id/debug", handler.DebugModel)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/models/company-model/debug", nil)
	engine.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusForbidden, recorder.Code)
}
