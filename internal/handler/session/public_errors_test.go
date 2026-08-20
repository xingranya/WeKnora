package session

import (
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

func TestBuildStreamResponseHidesErrorContentAndUntrustedMetadata(t *testing.T) {
	rawError := `upstream 401 body={"message":"客户模型失败","api_key":"private-provider-key"}`
	response := buildStreamResponse(interfaces.StreamEvent{
		Type:    types.ResponseTypeError,
		Content: rawError,
		Done:    true,
		Data: map[string]interface{}{
			"stage":       "model_stream",
			"code":        "UPSTREAM_UNAVAILABLE",
			"retryable":   true,
			"error":       rawError,
			"body":        "private response body",
			"details":     map[string]interface{}{"token": "nested-secret"},
			"unsafeStage": "not copied",
		},
	}, "req-1")

	if response.Content != publicSessionFailureMessage {
		t.Fatalf("公开错误正文 = %q", response.Content)
	}
	if response.Data["stage"] != "model_stream" || response.Data["code"] != "UPSTREAM_UNAVAILABLE" || response.Data["retryable"] != true {
		t.Fatalf("安全操作元数据未保留: %#v", response.Data)
	}
	if response.Data["error"] != publicSessionFailureMessage {
		t.Fatalf("公开 error 字段未收敛: %#v", response.Data)
	}
	serialized := response.Content
	for key, value := range response.Data {
		serialized += key + "="
		if text, ok := value.(string); ok {
			serialized += text
		}
	}
	for _, forbidden := range []string{"private-provider-key", "private response body", "nested-secret", "客户模型失败", rawError} {
		if strings.Contains(serialized, forbidden) {
			t.Fatalf("公开错误响应泄露 %q: %s %#v", forbidden, response.Content, response.Data)
		}
	}
	if _, ok := response.Data["body"]; ok {
		t.Fatalf("公开错误响应保留了 body: %#v", response.Data)
	}
	if _, ok := response.Data["details"]; ok {
		t.Fatalf("公开错误响应保留了 details: %#v", response.Data)
	}
}

func TestPublicSessionErrorDataRejectsFreeFormStage(t *testing.T) {
	data := publicSessionErrorData(map[string]interface{}{
		"stage": `model failed api_key=private-provider-key`,
	})
	if _, ok := data["stage"]; ok {
		t.Fatalf("自由文本 stage 不应回传: %#v", data)
	}
}
