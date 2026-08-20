package utils

import (
	"strings"
	"testing"
)

func TestSanitizeAuditLogPreservesBusinessContentAndRedactsCredentials(t *testing.T) {
	input := `员工问题：总结客户方案；url=https://docs.example.com/page?api_key=secret-value；Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.signature；key=普通业务字段`
	got := SanitizeAuditLog(input)

	for _, businessText := range []string{"员工问题：总结客户方案", "https://docs.example.com/page", "key=普通业务字段"} {
		if !strings.Contains(got, businessText) {
			t.Fatalf("审计日志丢失业务文本 %q: %s", businessText, got)
		}
	}
	for _, credential := range []string{"secret-value", "eyJhbGciOiJIUzI1NiJ9", "signature"} {
		if strings.Contains(got, credential) {
			t.Fatalf("审计日志泄露凭据 %q: %s", credential, got)
		}
	}
}

func TestSanitizeAuditLogRedactsJSONSecretsAndAPIKeys(t *testing.T) {
	input := `{"password":"pass with spaces","client_secret":"client-456","content":"员工上传了报价单","provider_key":"not-a-credential"} sk-abcdefghijk`
	got := SanitizeAuditLog(input)

	if !strings.Contains(got, "员工上传了报价单") || !strings.Contains(got, "provider_key") {
		t.Fatalf("审计日志没有保留业务字段: %s", got)
	}
	for _, credential := range []string{"pass with spaces", "client-456", "sk-abcdefghijk"} {
		if strings.Contains(got, credential) {
			t.Fatalf("审计日志泄露凭据 %q: %s", credential, got)
		}
	}
}

func TestSanitizeAuditLogRedactsOpaqueTokensAndURLUserInfo(t *testing.T) {
	input := `{"token":"opaque value","id_token":"identity"} https://user:pass@example.com/path?signature=signed-value xoxb-1234567890-secret github_pat_abcdefghijk`
	got := SanitizeAuditLog(input)
	for _, credential := range []string{"opaque value", "identity", "user:pass", "signed-value", "xoxb-1234567890-secret", "github_pat_abcdefghijk"} {
		if strings.Contains(got, credential) {
			t.Fatalf("审计日志泄露凭据 %q: %s", credential, got)
		}
	}
	if !strings.Contains(got, "example.com/path") {
		t.Fatalf("审计日志丢失 URL 业务信息: %s", got)
	}
	if !strings.Contains(got, "https://user:[REDACTED]@example.com/path") {
		t.Fatalf("审计日志没有保留 URL 用户名和路径: %s", got)
	}
}

func TestSanitizeAuditLogPreservesBusinessResourceTokensAndURLs(t *testing.T) {
	input := `folder_token=folder-123 document_token=doc-456 key=业务键 image_url=https://cdn.example.com/产品图.png model_result="报价结论" token=auth-secret`
	got := SanitizeAuditLog(input)

	for _, businessText := range []string{
		"folder_token=folder-123",
		"document_token=doc-456",
		"key=业务键",
		"image_url=https://cdn.example.com/产品图.png",
		`model_result="报价结论"`,
	} {
		if !strings.Contains(got, businessText) {
			t.Fatalf("审计日志误删业务字段 %q: %s", businessText, got)
		}
	}
	if strings.Contains(got, "auth-secret") {
		t.Fatalf("审计日志泄露裸 token 凭据: %s", got)
	}
}

func TestRedactAuditSecretsPreservesStructureAndRedactsMultilineKeys(t *testing.T) {
	input := "正文第一行\n正文第二行\n-----BEGIN PRIVATE KEY-----\nsecret-material\n-----END PRIVATE KEY-----"
	got := RedactAuditSecrets(input)

	if !strings.Contains(got, "正文第一行\n正文第二行") {
		t.Fatalf("专用审计日志丢失换行结构: %q", got)
	}
	if strings.Contains(got, "secret-material") || !strings.Contains(got, "[REDACTED_PRIVATE_KEY]") {
		t.Fatalf("专用审计日志未遮蔽多行私钥: %q", got)
	}
}

func TestSanitizeAuditLogRedactsBasicAuthorizationAndEscapedJSONSecret(t *testing.T) {
	input := `Authorization: Basic dXNlcjpwYXNz {"password":"quoted\"secret","content":"业务正文"}`
	got := SanitizeAuditLog(input)

	for _, secret := range []string{"dXNlcjpwYXNz", `quoted\"secret`} {
		if strings.Contains(got, secret) {
			t.Fatalf("审计日志泄露凭据 %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, "业务正文") {
		t.Fatalf("审计日志丢失业务正文: %s", got)
	}
}

func TestIsAuditSecretFieldNameUsesExactCredentialNames(t *testing.T) {
	for _, field := range []string{"api_key", "apiKey", "mineru_api_key", "password", "new_password", "publish_token", "SYSTEM_AES_KEY", "AWS_SECRET_ACCESS_KEY", "access_key_id", "secret_access_key", "Authorization"} {
		if !IsAuditSecretFieldName(field) {
			t.Fatalf("凭据字段 %q 未识别", field)
		}
	}
	for _, field := range []string{"folder_token", "document_token", "provider_key", "key", "image_url", "model_result"} {
		if IsAuditSecretFieldName(field) {
			t.Fatalf("业务字段 %q 被误判为凭据", field)
		}
	}
}
