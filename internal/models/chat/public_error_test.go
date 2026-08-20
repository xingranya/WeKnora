package chat

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

func TestPublicModelStreamFailureNeverReturnsUpstreamBody(t *testing.T) {
	upstream := `quota failed for 客户模型; api_key=private-provider-key`
	response := publicModelStreamFailure(context.Background(), "test", errors.New(upstream))

	if response.ResponseType != types.ResponseTypeError || !response.Done {
		t.Fatalf("错误流终态不完整: %#v", response)
	}
	if response.Content != publicModelStreamErrorMessage || strings.Contains(response.Content, upstream) {
		t.Fatalf("错误正文未收敛: %#v", response)
	}
}

func TestProcessAnthropicStreamHidesRawUpstreamError(t *testing.T) {
	rawSecret := "provider rejected request api_key=private-provider-key"
	body := `data: {"type":"error","error":{"type":"authentication_error","message":"` + rawSecret + `"}}` + "\n\n"
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(body))}
	stream := make(chan types.StreamResponse, 2)

	processAnthropicStream(context.Background(), "company-model", resp, stream)
	result, ok := <-stream
	if !ok {
		t.Fatal("错误流没有返回终态")
	}
	if result.Content != publicModelStreamErrorMessage || strings.Contains(result.Content, rawSecret) || strings.Contains(result.Content, "private-provider-key") {
		t.Fatalf("Anthropic 原始错误泄露给客户端: %#v", result)
	}
}

func TestStreamPacketDumperUsesPrivatePermissionsAndSanitizesAllRecordTypes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "raw")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WEKNORA_LLM_STREAM_RAW_DUMP_DIR", dir)
	t.Setenv("WEKNORA_LLM_STREAM_RAW_DUMP", "")

	dumper := newStreamPacketDumper("company/model", map[string]any{
		"content": "员工正文",
		"api_key": "request-secret",
	})
	if dumper == nil {
		t.Fatal("newStreamPacketDumper returned nil")
	}
	dumper.WritePacketRaw([]byte(`{"content":"模型解析结果","access_token":"packet-secret"}`))
	dumper.WriteError("上游业务错误 password=error-secret")
	dumper.WriteHTTPError(http.StatusUnauthorized, []byte(`{"summary":"供应商错误摘要","client_secret":"body-secret"}`))
	path := dumper.Path()
	dumper.Close()

	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("原始流目录权限 = %o, want 700", got)
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("原始流日志权限 = %o, want 600", got)
	}
	bodyBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(bodyBytes)
	for _, businessText := range []string{"员工正文", "模型解析结果", "上游业务错误", "供应商错误摘要"} {
		if !strings.Contains(got, businessText) {
			t.Fatalf("原始流日志丢失业务内容 %q: %s", businessText, got)
		}
	}
	for _, secret := range []string{"request-secret", "packet-secret", "error-secret", "body-secret"} {
		if strings.Contains(got, secret) {
			t.Fatalf("原始流日志泄露凭据 %q: %s", secret, got)
		}
	}
}
