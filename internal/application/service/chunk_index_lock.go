package service

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/Tencent/WeKnora/internal/common/redislock"
	"github.com/redis/go-redis/v9"
)

const (
	chunkIndexLockLease         = 2 * time.Minute
	chunkIndexLockRenewInterval = 30 * time.Second
)

type chunkIndexLockEntry struct {
	gate chan struct{}
	refs int
}

type chunkIndexLockManager struct {
	mu    sync.Mutex
	locks map[string]*chunkIndexLockEntry
}

var sharedChunkIndexLocks = chunkIndexLockManager{
	locks: make(map[string]*chunkIndexLockEntry),
}

func (m *chunkIndexLockManager) acquire(ctx context.Context, key string) (func(), error) {
	m.mu.Lock()
	entry := m.locks[key]
	if entry == nil {
		entry = &chunkIndexLockEntry{gate: make(chan struct{}, 1)}
		m.locks[key] = entry
	}
	entry.refs++
	m.mu.Unlock()

	select {
	case entry.gate <- struct{}{}:
		return m.release(key, entry), nil
	case <-ctx.Done():
		m.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(m.locks, key)
		}
		m.mu.Unlock()
		return nil, ctx.Err()
	}
}

func (m *chunkIndexLockManager) release(key string, entry *chunkIndexLockEntry) func() {
	return func() {
		<-entry.gate
		m.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(m.locks, key)
		}
		m.mu.Unlock()
	}
}

// withChunkIndexLock 串行化同一租户同一 chunk 的正文、元数据和向量变更。
// Redis 可用时跨实例加锁，Lite 模式退化为当前进程内的 keyed mutex。
func withChunkIndexLock(
	ctx context.Context,
	redisClient *redis.Client,
	tenantID uint64,
	chunkID string,
	fn func(context.Context) error,
) error {
	return withChunkIndexLocks(ctx, redisClient, tenantID, []string{chunkID}, fn)
}

// withChunkIndexLocks 按稳定顺序获取多个 chunk 锁，避免批量索引互相死锁。
func withChunkIndexLocks(
	ctx context.Context,
	redisClient *redis.Client,
	tenantID uint64,
	chunkIDs []string,
	fn func(context.Context) error,
) error {
	if fn == nil {
		return fmt.Errorf("chunk index lock callback is required")
	}
	keys := make([]string, 0, len(chunkIDs))
	seen := make(map[string]struct{}, len(chunkIDs))
	for _, chunkID := range chunkIDs {
		if chunkID == "" {
			continue
		}
		key := fmt.Sprintf("%d:%s", tenantID, chunkID)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return fn(ctx)
	}

	releases := make([]func(), 0, len(keys))
	defer func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}()
	for _, key := range keys {
		release, err := sharedChunkIndexLocks.acquire(ctx, key)
		if err != nil {
			return err
		}
		releases = append(releases, release)
	}

	var withRedisLocks func(int, context.Context) error
	withRedisLocks = func(index int, lockCtx context.Context) error {
		if index == len(keys) || redisClient == nil {
			return fn(lockCtx)
		}
		return redislock.WithRenewableLock(
			lockCtx,
			redisClient,
			"weknora:chunk-index:"+keys[index],
			chunkIndexLockLease,
			chunkIndexLockRenewInterval,
			func(nextCtx context.Context) error {
				return withRedisLocks(index+1, nextCtx)
			},
		)
	}
	return withRedisLocks(0, ctx)
}
