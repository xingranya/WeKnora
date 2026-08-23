package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func newKnowledgeTagsRequestServer(t *testing.T, requestBody chan<- []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("method = %s, want PUT", r.Method)
		}
		if r.URL.Path != "/api/v1/knowledge/tags" {
			t.Errorf("path = %s, want /api/v1/knowledge/tags", r.URL.Path)
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		requestBody <- raw
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"success": true}); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
}

func TestBatchReplaceKnowledgeTagsSendsMultipleTagsAndExactEmptyArray(t *testing.T) {
	requestBody := make(chan []byte, 1)
	server := newKnowledgeTagsRequestServer(t, requestBody)
	defer server.Close()

	client := NewClient(server.URL, WithAPIKey("sk-test"))
	err := client.BatchReplaceKnowledgeTags(context.Background(), map[string][]string{
		"knowledge-multiple": {"tag-a", "tag-b"},
		"knowledge-clear":    {},
		"knowledge-nil":      nil,
	})
	if err != nil {
		t.Fatalf("BatchReplaceKnowledgeTags() error = %v", err)
	}

	var request struct {
		Updates map[string]json.RawMessage `json:"updates"`
	}
	if err := json.Unmarshal(<-requestBody, &request); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if got := string(request.Updates["knowledge-multiple"]); got != `["tag-a","tag-b"]` {
		t.Errorf("multiple tags = %s, want [\"tag-a\",\"tag-b\"]", got)
	}
	if got := string(request.Updates["knowledge-clear"]); got != `[]` {
		t.Errorf("clear tags = %s, want []", got)
	}
	if got := string(request.Updates["knowledge-nil"]); got != `[]` {
		t.Errorf("nil tags = %s, want []", got)
	}
}

func TestBatchUpdateKnowledgeTagsPreservesLegacySignatureAndSendsArrayContract(t *testing.T) {
	requestBody := make(chan []byte, 1)
	server := newKnowledgeTagsRequestServer(t, requestBody)
	defer server.Close()

	tagID := "tag-a"
	emptyTagID := ""
	client := NewClient(server.URL, WithAPIKey("sk-test"))
	legacyRequest := BatchUpdateKnowledgeTagsRequest{Updates: map[string]*string{
		"knowledge-single": &tagID,
		"knowledge-clear":  nil,
		"knowledge-empty":  &emptyTagID,
	}}
	err := client.BatchUpdateKnowledgeTags(context.Background(), legacyRequest.Updates)
	if err != nil {
		t.Fatalf("BatchUpdateKnowledgeTags() error = %v", err)
	}

	var request BatchReplaceKnowledgeTagsRequest
	if err := json.Unmarshal(<-requestBody, &request); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if got := request.Updates["knowledge-single"]; !reflect.DeepEqual(got, []string{"tag-a"}) {
		t.Errorf("single tags = %#v, want []string{\"tag-a\"}", got)
	}
	if got := request.Updates["knowledge-clear"]; got == nil || len(got) != 0 {
		t.Errorf("clear tags = %#v, want non-nil empty slice", got)
	}
	if got := request.Updates["knowledge-empty"]; got == nil || len(got) != 0 {
		t.Errorf("empty tag = %#v, want non-nil empty slice", got)
	}
}
