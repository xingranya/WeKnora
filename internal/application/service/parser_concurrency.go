package service

import (
	"context"
	"os"
	"strconv"
	"strings"

	"github.com/Tencent/WeKnora/internal/infrastructure/docparser"
)

const (
	defaultDocReaderConcurrency = 4
	docReaderConcurrencyGateKey = "docreader_remote"
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

func docReaderConcurrencyLimit() int {
	workerLimit := defaultDocReaderConcurrency
	if raw := strings.TrimSpace(os.Getenv("DOCREADER_GRPC_MAX_WORKERS")); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil && value > 0 {
			workerLimit = value
		}
	}
	limit := workerLimit
	if raw := strings.TrimSpace(os.Getenv("WEKNORA_DOCREADER_MAX_CONCURRENCY")); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil && value > 0 && value < limit {
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
	return gateParserReadForRoute(ctx, parserEngine, "", false, overrides)
}

func gateParserReadForRoute(
	ctx context.Context,
	parserEngine string,
	fileType string,
	isURL bool,
	overrides map[string]string,
) (context.Context, func(), error) {
	if parserEngine != docparser.MinerUEngineName {
		if docparser.UsesRemoteDocReader(parserEngine, fileType, isURL) {
			return docparser.GateParser(ctx, docReaderConcurrencyGateKey, docReaderConcurrencyLimit())
		}
		return ctx, func() {}, nil
	}
	return docparser.GateParser(ctx, parserEngine, parserConcurrencyLimit(overrides))
}
