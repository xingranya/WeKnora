package router

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSyncTaskExecutorConcurrentTaskIDHasSingleWinner(t *testing.T) {
	executor := NewSyncTaskExecutor()
	started := make(chan struct{})
	release := make(chan struct{})
	completed := make(chan struct{})
	var startOnce sync.Once
	executor.RegisterHandler("test:task-id-single-winner", func(context.Context, *asynq.Task) error {
		startOnce.Do(func() { close(started) })
		<-release
		close(completed)
		return nil
	})

	const callers = 32
	results := make(chan error, callers)
	var callersWG sync.WaitGroup
	callersWG.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer callersWG.Done()
			_, err := executor.Enqueue(
				asynq.NewTask("test:task-id-single-winner", nil),
				asynq.TaskID("shared-task-id"),
			)
			results <- err
		}()
	}
	callersWG.Wait()
	close(results)

	winners := 0
	conflicts := 0
	for err := range results {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, asynq.ErrTaskIDConflict):
			conflicts++
		default:
			t.Fatalf("unexpected enqueue error: %v", err)
		}
	}
	assert.Equal(t, 1, winners)
	assert.Equal(t, callers-1, conflicts)

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("accepted task did not start")
	}
	close(release)
	select {
	case <-completed:
	case <-time.After(2 * time.Second):
		t.Fatal("accepted task did not complete")
	}
}

func TestSyncTaskExecutorAcceptsDifferentTaskIDs(t *testing.T) {
	executor := NewSyncTaskExecutor()
	completed := make(chan struct{}, 12)
	executor.RegisterHandler("test:distinct-task-ids", func(context.Context, *asynq.Task) error {
		completed <- struct{}{}
		return nil
	})

	for i := 0; i < cap(completed); i++ {
		wantID := fmt.Sprintf("distinct-%d", i)
		info, err := executor.Enqueue(
			asynq.NewTask("test:distinct-task-ids", nil),
			asynq.TaskID(wantID),
		)
		require.NoError(t, err)
		assert.Equal(t, wantID, info.ID)
	}

	for i := 0; i < cap(completed); i++ {
		select {
		case <-completed:
		case <-time.After(2 * time.Second):
			t.Fatal("distinct task did not complete")
		}
	}
}

func TestSyncTaskExecutorReleasesTaskIDAfterSuccessAndFinalFailure(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		executor := NewSyncTaskExecutor()
		runs := make(chan struct{}, 2)
		executor.RegisterHandler("test:successful-lifecycle", func(context.Context, *asynq.Task) error {
			runs <- struct{}{}
			return nil
		})

		_, err := executor.Enqueue(
			asynq.NewTask("test:successful-lifecycle", nil),
			asynq.TaskID("reusable-success"),
		)
		require.NoError(t, err)
		<-runs
		require.Eventually(t, func() bool {
			_, retryErr := executor.Enqueue(
				asynq.NewTask("test:successful-lifecycle", nil),
				asynq.TaskID("reusable-success"),
			)
			return retryErr == nil
		}, 2*time.Second, 10*time.Millisecond)
		<-runs
	})

	t.Run("final failure", func(t *testing.T) {
		executor := NewSyncTaskExecutor()
		runs := make(chan struct{}, 2)
		executor.RegisterHandler("test:failed-lifecycle", func(context.Context, *asynq.Task) error {
			runs <- struct{}{}
			return errors.New("terminal failure")
		})

		_, err := executor.Enqueue(
			asynq.NewTask("test:failed-lifecycle", nil),
			asynq.TaskID("reusable-failure"),
			asynq.MaxRetry(0),
		)
		require.NoError(t, err)
		<-runs
		require.Eventually(t, func() bool {
			_, retryErr := executor.Enqueue(
				asynq.NewTask("test:failed-lifecycle", nil),
				asynq.TaskID("reusable-failure"),
				asynq.MaxRetry(0),
			)
			return retryErr == nil
		}, 2*time.Second, 10*time.Millisecond)
		<-runs
	})

	t.Run("skip retry", func(t *testing.T) {
		executor := NewSyncTaskExecutor()
		var runs atomic.Int32
		executor.RegisterHandler("test:skip-retry-lifecycle", func(context.Context, *asynq.Task) error {
			runs.Add(1)
			return fmt.Errorf("invalid payload: %w", asynq.SkipRetry)
		})

		_, err := executor.Enqueue(
			asynq.NewTask("test:skip-retry-lifecycle", nil),
			asynq.TaskID("reusable-skip-retry"),
			asynq.MaxRetry(5),
		)
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			_, retryErr := executor.Enqueue(
				asynq.NewTask("test:skip-retry-lifecycle", nil),
				asynq.TaskID("reusable-skip-retry"),
				asynq.MaxRetry(0),
			)
			return retryErr == nil
		}, 2*time.Second, 10*time.Millisecond)
		require.Eventually(t, func() bool { return runs.Load() == 2 }, 2*time.Second, 10*time.Millisecond)
	})
}

func TestSyncTaskExecutorReadsEmbeddedOptionsAndAllowsEnqueueOverrides(t *testing.T) {
	t.Run("embedded process-in", func(t *testing.T) {
		executor := NewSyncTaskExecutor()
		startedAt := make(chan time.Time, 1)
		executor.RegisterHandler("test:embedded-delay", func(context.Context, *asynq.Task) error {
			startedAt <- time.Now()
			return nil
		})

		start := time.Now()
		info, err := executor.Enqueue(asynq.NewTask(
			"test:embedded-delay",
			nil,
			asynq.TaskID("embedded-delay-id"),
			asynq.ProcessIn(30*time.Millisecond),
			asynq.MaxRetry(0),
		))
		require.NoError(t, err)
		assert.Equal(t, "embedded-delay-id", info.ID)

		select {
		case observed := <-startedAt:
			assert.GreaterOrEqual(t, observed.Sub(start), 25*time.Millisecond)
			assert.Less(t, observed.Sub(start), time.Second)
		case <-time.After(2 * time.Second):
			t.Fatal("task using embedded delay did not start")
		}
	})

	t.Run("enqueue options override embedded options", func(t *testing.T) {
		executor := NewSyncTaskExecutor()
		startedAt := make(chan time.Time, 1)
		executor.RegisterHandler("test:overridden-options", func(context.Context, *asynq.Task) error {
			startedAt <- time.Now()
			return nil
		})

		start := time.Now()
		info, err := executor.Enqueue(asynq.NewTask(
			"test:overridden-options",
			nil,
			asynq.TaskID("embedded-id"),
			asynq.ProcessIn(time.Hour),
			asynq.MaxRetry(9),
		), asynq.TaskID("override-id"), asynq.ProcessIn(20*time.Millisecond), asynq.MaxRetry(0))
		require.NoError(t, err)
		assert.Equal(t, "override-id", info.ID)

		select {
		case observed := <-startedAt:
			assert.GreaterOrEqual(t, observed.Sub(start), 15*time.Millisecond)
			assert.Less(t, observed.Sub(start), time.Second)
		case <-time.After(2 * time.Second):
			t.Fatal("task using overridden options did not start")
		}
	})

	t.Run("embedded max-retry zero", func(t *testing.T) {
		executor := NewSyncTaskExecutor()
		var runs atomic.Int32
		executor.RegisterHandler("test:embedded-max-retry", func(context.Context, *asynq.Task) error {
			runs.Add(1)
			return errors.New("terminal failure")
		})

		_, err := executor.Enqueue(asynq.NewTask(
			"test:embedded-max-retry",
			nil,
			asynq.TaskID("embedded-max-retry-id"),
			asynq.MaxRetry(0),
		))
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			_, retryErr := executor.Enqueue(asynq.NewTask(
				"test:embedded-max-retry",
				nil,
				asynq.TaskID("embedded-max-retry-id"),
				asynq.MaxRetry(0),
			))
			return retryErr == nil
		}, 2*time.Second, 10*time.Millisecond)
		require.Eventually(t, func() bool { return runs.Load() == 2 }, 2*time.Second, 10*time.Millisecond)
	})
}

func TestSyncTaskExecutorMoveRetryAcceptsOnlyOneChildTask(t *testing.T) {
	executor := NewSyncTaskExecutor()
	started := make(chan struct{})
	release := make(chan struct{})
	completed := make(chan struct{})
	var executions atomic.Int32
	executor.RegisterHandler("test:move-reparse-child", func(context.Context, *asynq.Task) error {
		executions.Add(1)
		close(started)
		<-release
		close(completed)
		return nil
	})

	// 模拟移动父任务先成功投递子任务、随后数据库提交失败并重试。
	// 稳定 TaskID 放在 NewTask 上，与真实 reparse 路径保持一致。
	enqueueChild := func() error {
		_, err := executor.Enqueue(asynq.NewTask(
			"test:move-reparse-child",
			nil,
			asynq.TaskID("knowledge-move-reparse-task-doc"),
			asynq.MaxRetry(0),
		))
		if errors.Is(err, asynq.ErrTaskIDConflict) || errors.Is(err, asynq.ErrDuplicateTask) {
			return nil
		}
		return err
	}

	require.NoError(t, enqueueChild())
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("move child task did not start")
	}
	require.NoError(t, enqueueChild())
	assert.Equal(t, int32(1), executions.Load())

	close(release)
	select {
	case <-completed:
	case <-time.After(2 * time.Second):
		t.Fatal("move child task did not complete")
	}
}
