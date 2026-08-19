package service

import (
	"context"
	"os"
	"strconv"
	"strings"

	"github.com/Tencent/WeKnora/internal/infrastructure/docparser"
)

func parserConcurrencyLimit(overrides map[string]string) int {
	limit := 1
	if raw := strings.TrimSpace(overrides["mineru_max_concurrency"]); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil && value > 0 {
			limit = value
		}
	}
	if raw := strings.TrimSpace(os.Getenv("WEKNORA_MINERU_MAX_CONCURRENCY")); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil && value > 0 {
			limit = value
		}
	}
	return limit
}

// gateParserRead 让知识库和临时附件共享同一个解析引擎并发租约。
func gateParserRead(
	ctx context.Context,
	parserEngine string,
	overrides map[string]string,
) (context.Context, func(), error) {
	if parserEngine != docparser.MinerUEngineName {
		return ctx, func() {}, nil
	}
	return docparser.GateParser(ctx, parserEngine, parserConcurrencyLimit(overrides))
}
