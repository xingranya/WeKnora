package docparser

import (
	"context"
	"sync"

	modellimiter "github.com/Tencent/WeKnora/internal/models/limiter"
	"github.com/redis/go-redis/v9"
)

var parserGovernor struct {
	sync.RWMutex
	limiter modellimiter.ModelConcurrencyLimiter
}

// ConfigureConcurrency 配置按解析引擎隔离的信号量；标准模式使用 Redis，
// Lite 模式使用进程内信号量。
func ConfigureConcurrency(redisClient *redis.Client) {
	parserGovernor.Lock()
	defer parserGovernor.Unlock()
	if redisClient != nil {
		parserGovernor.limiter = modellimiter.NewStrictRedisLimiter(redisClient)
	} else {
		parserGovernor.limiter = modellimiter.NewLocalLimiter()
	}
}

func GateParser(ctx context.Context, engine string, limit int) (context.Context, func(), error) {
	parserGovernor.RLock()
	limiter := parserGovernor.limiter
	parserGovernor.RUnlock()
	if limiter == nil || engine == "" || limit <= 0 {
		return ctx, func() {}, nil
	}
	type leaseAware interface {
		AcquireLease(context.Context, string, int) (context.Context, func(), error)
	}
	if aware, ok := limiter.(leaseAware); ok {
		leaseCtx, release, err := aware.AcquireLease(ctx, "parser:"+engine, limit)
		if err != nil {
			return nil, nil, err
		}
		if release == nil {
			release = func() {}
		}
		if leaseCtx == nil {
			leaseCtx = ctx
		}
		if err := leaseCtx.Err(); err != nil {
			release()
			return nil, nil, err
		}
		return leaseCtx, release, nil
	}
	release, err := limiter.Acquire(ctx, "parser:"+engine, limit)
	if err != nil {
		return nil, nil, err
	}
	if release == nil {
		release = func() {}
	}
	if err := ctx.Err(); err != nil {
		release()
		return nil, nil, err
	}
	return ctx, release, nil
}

func ParserConcurrencyStats(ctx context.Context) ([]modellimiter.RuntimeStat, bool, error) {
	parserGovernor.RLock()
	limiter := parserGovernor.limiter
	parserGovernor.RUnlock()
	type inspector interface {
		RuntimeStats(context.Context) ([]modellimiter.RuntimeStat, error)
	}
	value, ok := limiter.(inspector)
	if !ok || value == nil {
		return []modellimiter.RuntimeStat{}, false, nil
	}
	stats, err := value.RuntimeStats(ctx)
	if stats == nil {
		stats = []modellimiter.RuntimeStat{}
	}
	return stats, true, err
}
