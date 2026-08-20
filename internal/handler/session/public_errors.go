package session

import (
	"context"
	"strings"

	"github.com/Tencent/WeKnora/internal/logger"
)

// 公开错误响应只保留稳定文案；上游正文、响应体和认证信息仅进入内部审计日志。
const publicSessionFailureMessage = "Answer generation failed. Please retry."

func logPrivateSessionError(ctx context.Context, stage, errorText string) {
	if strings.TrimSpace(errorText) == "" || errorText == publicSessionFailureMessage {
		return
	}
	logger.Errorf(ctx, "[SessionStream] stage=%s error=%q", stage, logger.AuditText(errorText, 4096))
}

func isSafePublicErrorLabel(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '_' || r == '-' || r == '.' || r == ':' {
			continue
		}
		return false
	}
	return true
}

// publicSessionErrorData 对错误事件元数据采用白名单，防止 error/body/details
// 等上游字段绕过 Content 边界回传。stage/code 仅接受短标识符。
func publicSessionErrorData(data map[string]interface{}) map[string]interface{} {
	result := map[string]interface{}{"error": publicSessionFailureMessage}
	for _, key := range []string{"stage", "code", "tool_name", "tool_call_id", "event_id"} {
		if value, ok := data[key].(string); ok && isSafePublicErrorLabel(value) {
			result[key] = value
		}
	}
	if retryable, ok := data["retryable"].(bool); ok {
		result["retryable"] = retryable
	}
	if success, ok := data["success"].(bool); ok {
		result["success"] = success
	}
	if duration, ok := data["duration_ms"].(int64); ok && duration >= 0 {
		result["duration_ms"] = duration
	}
	return result
}
