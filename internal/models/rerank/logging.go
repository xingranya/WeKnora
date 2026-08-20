package rerank

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/logger"
)

const (
	maxLogDocuments = 3
	maxLogTextRunes = 120
)

func buildRerankRequestDebug(model, endpoint, query string, documents []string) string {
	totalDocumentRunes := 0
	previews := make([]string, 0, maxLogDocuments)
	for index, document := range documents {
		totalDocumentRunes += utf8.RuneCountInString(document)
		if index < maxLogDocuments {
			previews = append(previews, compactForAuditLog(document, maxLogTextRunes))
		}
	}
	previewJSON, _ := json.Marshal(previews)
	return fmt.Sprintf(
		"rerank request endpoint=%s model=%s query_preview=%q query_runes=%d documents=%d document_runes=%d preview_docs=%s",
		logger.AuditText(endpoint, 512),
		model,
		compactForAuditLog(query, maxLogTextRunes),
		utf8.RuneCountInString(query),
		len(documents),
		totalDocumentRunes,
		string(previewJSON),
	)
}

func compactForAuditLog(text string, maxRunes int) string {
	normalized := strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	return logger.AuditText(normalized, maxRunes)
}
