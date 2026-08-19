package service

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGenerateQuestionBatchConcurrentlyRespectsLimitAndOrder(t *testing.T) {
	t.Parallel()

	var active int32
	var peak int32
	results, err := generateQuestionBatchConcurrently(
		context.Background(),
		12,
		3,
		func(_ context.Context, index int) ([]string, error) {
			current := atomic.AddInt32(&active, 1)
			defer atomic.AddInt32(&active, -1)
			for {
				observed := atomic.LoadInt32(&peak)
				if current <= observed || atomic.CompareAndSwapInt32(&peak, observed, current) {
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
			return []string{string(rune('a' + index))}, nil
		},
	)

	require.NoError(t, err)
	require.LessOrEqual(t, peak, int32(3))
	require.Greater(t, peak, int32(1))
	for index, result := range results {
		require.NoError(t, result.err)
		require.Equal(t, []string{string(rune('a' + index))}, result.questions)
	}
}

func TestGenerateQuestionBatchConcurrentlyKeepsSiblingResultsOnFailure(t *testing.T) {
	t.Parallel()

	expectedErr := errors.New("model unavailable")
	results, err := generateQuestionBatchConcurrently(
		context.Background(),
		4,
		2,
		func(_ context.Context, index int) ([]string, error) {
			if index == 1 {
				return nil, expectedErr
			}
			return []string{"ok"}, nil
		},
	)

	require.NoError(t, err)
	require.ErrorIs(t, results[1].err, expectedErr)
	require.Equal(t, []string{"ok"}, results[0].questions)
	require.Equal(t, []string{"ok"}, results[2].questions)
	require.Equal(t, []string{"ok"}, results[3].questions)
}

func TestGenerateQuestionBatchConcurrentlyReturnsContextCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	results, err := generateQuestionBatchConcurrently(
		ctx,
		3,
		2,
		func(generateCtx context.Context, index int) ([]string, error) {
			if index == 0 {
				cancel()
			}
			<-generateCtx.Done()
			return nil, generateCtx.Err()
		},
	)

	require.ErrorIs(t, err, context.Canceled)
	require.Len(t, results, 3)
}
