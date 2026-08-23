package container

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

type pendingKnowledgeMoveRecoverer interface {
	RecoverPendingKnowledgeMoves(context.Context, int, bool) error
}

// recoverPendingKnowledgeMoves 在所有 Redis/Lite handler 注册后，从持久
// outbox 强制恢复一次知识移动任务。稳定 TaskID 会折叠仍存活的 Redis 任务。
func recoverPendingKnowledgeMoves(knowledgeService interfaces.KnowledgeService) {
	recoverer, ok := knowledgeService.(pendingKnowledgeMoveRecoverer)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := recoverer.RecoverPendingKnowledgeMoves(ctx, 200, true); err != nil {
		logger.Warnf(ctx, "[MoveRecovery] startup recovery failed: %v", err)
	}
}
