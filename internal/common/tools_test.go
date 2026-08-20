package common

import (
	"strings"
	"testing"
)

func TestParseLLMJsonResponse(t *testing.T) {
	type payload struct {
		Key string `json:"key"`
	}

	tests := []struct {
		name    string
		content string
		want    string
		wantErr bool
	}{
		{"direct json", `{"key":"value"}`, "value", false},
		{"fenced json", "```json\n{\"key\":\"fenced\"}\n```", "fenced", false},
		{"fenced no lang", "```\n{\"key\":\"plain\"}\n```", "plain", false},
		{"prose around fence", "Here you go:\n```json\n{\"key\":\"wrapped\"}\n```\nThanks", "wrapped", false},
		{"prose around bare json", `Sure: {"key":"bare"} hope that helps`, "bare", false},
		// Trailing bracket-like prose must not truncate the payload.
		{"trailing citation", `{"key":"cited"} — see section [1].`, "cited", false},
		// Braces inside a string literal must not close the object early.
		{"braces in string", `Result: {"key":"a}b"} done`, "a}b", false},
		{"not json", "just some text", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got payload
			err := ParseLLMJsonResponse(tt.content, &got)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got.Key != tt.want {
				t.Errorf("Key = %q, want %q", got.Key, tt.want)
			}
		})
	}
}

func TestPipelineLogPreservesAuditContentButRedactsCredentials(t *testing.T) {
	logLine := PipelineLog("Stream", "messages_ready", map[string]interface{}{
		"request_id":     "req-123",
		"message_count":  2,
		"system_prompt":  "private system prompt",
		"user_message":   "private user question",
		"result_preview": []string{"private document"},
		"error":          "provider echoed private content with sk-abcdefghijk",
		"api_key":        "credential-must-not-leak",
	})

	for _, businessText := range []string{
		"private system prompt", "private user question", "private document", "provider echoed private content",
	} {
		if !strings.Contains(logLine, businessText) {
			t.Fatalf("PipelineLog missing audit content %q: %s", businessText, logLine)
		}
	}
	for _, secret := range []string{"sk-abcdefghijk", "credential-must-not-leak"} {
		if strings.Contains(logLine, secret) {
			t.Fatalf("PipelineLog leaked credential %q: %s", secret, logLine)
		}
	}
	for _, expected := range []string{"request_id=\"req-123\"", "message_count=2", "[redacted"} {
		if !strings.Contains(logLine, expected) {
			t.Fatalf("PipelineLog missing %q: %s", expected, logLine)
		}
	}
}

func BenchmarkParseLLMJsonResponse_Fenced(b *testing.B) {
	const content = "```json\n{\"name\":\"Acme\",\"slug\":\"entity/acme\",\"aliases\":[\"A\",\"B\"]}\n```"
	type payload struct {
		Name string `json:"name"`
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var p payload
		_ = ParseLLMJsonResponse(content, &p)
	}
}
