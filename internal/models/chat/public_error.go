package chat

import (
	"context"
	"fmt"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
)

// publicModelStreamErrorMessage 是模型流失败时唯一允许发给客户端的正文。
// 上游响应正文仍写入公司审计日志，但必须先经过统一凭据脱敏。
const publicModelStreamErrorMessage = "Model service is temporarily unavailable. Please retry."

func logPrivateModelError(ctx context.Context, source string, err error) {
	if err == nil {
		return
	}
	logger.Errorf(ctx, "[ModelStream] source=%s error=%q", source, logger.AuditText(err.Error(), 4096))
}

func publicModelStreamFailure(ctx context.Context, source string, err error) types.StreamResponse {
	logPrivateModelError(ctx, source, err)
	return types.StreamResponse{
		ResponseType: types.ResponseTypeError,
		Content:      publicModelStreamErrorMessage,
		Done:         true,
	}
}

func publicModelCallFailure(ctx context.Context, source string, err error) error {
	logPrivateModelError(ctx, source, err)
	return fmt.Errorf("%s", publicModelStreamErrorMessage)
}
