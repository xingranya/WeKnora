package middleware

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

func TestLoggerDoesNotRecordRequestOrResponseBodies(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var logs bytes.Buffer
	testLogger := logrus.New()
	testLogger.SetOutput(&logs)
	testLogger.SetFormatter(&logrus.JSONFormatter{DisableTimestamp: true})

	engine := gin.New()
	engine.ContextWithFallback = true
	engine.Use(withTestLogger(testLogger))
	engine.Use(Logger())
	engine.POST("/api/v1/storage-config", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"hmac_secret":       "response-hmac-secret",
			"mineru_api_key":    "response-mineru-key",
			"secret_access_key": "response-access-key",
			"publish_token":     "response-publish-token",
			"custom_headers":    gin.H{"Authorization": "response-custom-header"},
		})
	})

	requestBody := `{"hmac_secret":"request-hmac-secret","mineru_api_key":"request-mineru-key",` +
		`"secret_access_key":"request-access-key","publish_token":"request-publish-token",` +
		`"custom_headers":{"Authorization":"request-custom-header"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/storage-config", strings.NewReader(requestBody))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	logOutput := logs.String()
	for _, forbidden := range []string{
		"request_body",
		"response_body",
		"hmac_secret",
		"mineru_api_key",
		"secret_access_key",
		"publish_token",
		"custom_headers",
		"request-hmac-secret",
		"response-hmac-secret",
		"request-custom-header",
		"response-custom-header",
	} {
		if strings.Contains(logOutput, forbidden) {
			t.Fatalf("日志包含请求或响应正文片段 %q: %s", forbidden, logOutput)
		}
	}
}

func TestLoggerUsesRouteTemplateAndOmitsCapabilityQuery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var logs bytes.Buffer
	testLogger := logrus.New()
	testLogger.SetOutput(&logs)
	testLogger.SetFormatter(&logrus.JSONFormatter{DisableTimestamp: true})

	engine := gin.New()
	engine.ContextWithFallback = true
	engine.Use(withTestLogger(testLogger))
	engine.Use(Logger())
	engine.GET("/r/:token", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	req := httptest.NewRequest(
		http.MethodGet,
		"/r/capability-path-secret?sig=query-signature-secret&token=query-token-secret&safe=visible",
		nil,
	)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	logOutput := logs.String()
	for _, expected := range []string{"/r/:token"} {
		if !strings.Contains(logOutput, expected) {
			t.Fatalf("日志缺少 %q: %s", expected, logOutput)
		}
	}
	for _, forbidden := range []string{
		"capability-path-secret", "query-signature-secret", "query-token-secret",
		"sig=", "token=", "safe=visible",
	} {
		if strings.Contains(logOutput, forbidden) {
			t.Fatalf("日志泄漏 %q: %s", forbidden, logOutput)
		}
	}
}

func TestRequestIDRejectsPathLikeHeaderAndPropagatesGeneratedID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const maliciousID = "../../outside"
	var contextID, requestContextID string

	engine := gin.New()
	engine.Use(RequestID())
	engine.GET("/request-id", func(c *gin.Context) {
		contextID = c.GetString(types.RequestIDContextKey.String())
		if value, ok := c.Request.Context().Value(types.RequestIDContextKey).(string); ok {
			requestContextID = value
		}
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/request-id", nil)
	req.Header.Set("X-Request-ID", maliciousID)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	responseID := recorder.Header().Get("X-Request-ID")
	if responseID == "" || responseID == maliciousID || !safeRequestIDPattern.MatchString(responseID) {
		t.Fatalf("响应 Request-ID 不安全: %q", responseID)
	}
	if contextID != responseID || requestContextID != responseID {
		t.Fatalf("Request-ID 传播不一致: header=%q gin=%q request=%q", responseID, contextID, requestContextID)
	}
}

func TestRequestIDPreservesValidCallerID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const requestID = "corp-audit_req-42"
	engine := gin.New()
	engine.Use(RequestID())
	engine.GET("/request-id", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	req := httptest.NewRequest(http.MethodGet, "/request-id", nil)
	req.Header.Set("X-Request-ID", requestID)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	if got := recorder.Header().Get("X-Request-ID"); got != requestID {
		t.Fatalf("有效 Request-ID = %q, want %q", got, requestID)
	}
}

func withTestLogger(testLogger *logrus.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		entry := logrus.NewEntry(testLogger)
		ctx := context.WithValue(c.Request.Context(), types.LoggerContextKey, entry)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}
