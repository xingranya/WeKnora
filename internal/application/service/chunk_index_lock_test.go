package service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWithChunkIndexLockSerializesSameChunk(t *testing.T) {
	ctx := context.Background()
	started := make(chan struct{})
	secondEntered := make(chan struct{})
	release := make(chan struct{})
	var active atomic.Int32
	var maxActive atomic.Int32
	var wg sync.WaitGroup

	lockedWork := func(entered chan struct{}) func(context.Context) error {
		return func(context.Context) error {
			current := active.Add(1)
			for {
				old := maxActive.Load()
				if current <= old || maxActive.CompareAndSwap(old, current) {
					break
				}
			}
			close(entered)
			<-release
			active.Add(-1)
			return nil
		}
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := withChunkIndexLock(ctx, nil, 1, "chunk", lockedWork(started)); err != nil {
			t.Errorf("first lock failed: %v", err)
		}
	}()
	<-started

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := withChunkIndexLock(ctx, nil, 1, "chunk", lockedWork(secondEntered)); err != nil {
			t.Errorf("second lock failed: %v", err)
		}
	}()

	select {
	case <-secondEntered:
		t.Fatal("same chunk entered the critical section concurrently")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	wg.Wait()
	if got := maxActive.Load(); got != 1 {
		t.Fatalf("same chunk reached max concurrency %d, want 1", got)
	}
}

func TestWithChunkIndexLockAllowsDifferentChunksInParallel(t *testing.T) {
	ctx := context.Background()
	firstEntered := make(chan struct{})
	secondEntered := make(chan struct{})
	release := make(chan struct{})
	var wg sync.WaitGroup

	lockedWork := func(entered chan struct{}) func(context.Context) error {
		return func(context.Context) error {
			close(entered)
			<-release
			return nil
		}
	}

	for chunkID, entered := range map[string]chan struct{}{
		"chunk-a": firstEntered,
		"chunk-b": secondEntered,
	} {
		chunkID, entered := chunkID, entered
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := withChunkIndexLock(ctx, nil, 1, chunkID, lockedWork(entered)); err != nil {
				t.Errorf("lock for %s failed: %v", chunkID, err)
			}
		}()
	}

	select {
	case <-firstEntered:
	case <-time.After(time.Second):
		t.Fatal("first chunk did not enter critical section")
	}
	select {
	case <-secondEntered:
	case <-time.After(time.Second):
		t.Fatal("second chunk was unexpectedly serialized")
	}
	close(release)
	wg.Wait()
}

func TestWithChunkIndexLockHonorsCancellationWhileWaiting(t *testing.T) {
	ctx := context.Background()
	entered := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- withChunkIndexLock(ctx, nil, 1, "chunk", func(context.Context) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	waitCtx, cancel := context.WithCancel(ctx)
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- withChunkIndexLock(waitCtx, nil, 1, "chunk", func(context.Context) error {
			return nil
		})
	}()
	cancel()

	select {
	case err := <-secondDone:
		if err == nil {
			t.Fatal("canceled waiter unexpectedly entered the critical section")
		}
	case <-time.After(time.Second):
		t.Fatal("canceled waiter did not return")
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first lock failed: %v", err)
	}
}

func TestWithChunkIndexLocksSerializesOverlappingSets(t *testing.T) {
	ctx := context.Background()
	firstEntered := make(chan struct{})
	secondEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstDone := make(chan error, 1)
	secondDone := make(chan error, 1)

	go func() {
		firstDone <- withChunkIndexLocks(ctx, nil, 1, []string{"child-a", "parent"}, func(context.Context) error {
			close(firstEntered)
			<-releaseFirst
			return nil
		})
	}()
	<-firstEntered
	go func() {
		secondDone <- withChunkIndexLocks(ctx, nil, 1, []string{"parent", "child-b"}, func(context.Context) error {
			close(secondEntered)
			return nil
		})
	}()

	select {
	case <-secondEntered:
		t.Fatal("overlapping lock sets entered concurrently")
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("first lock set failed: %v", err)
	}
	select {
	case <-secondEntered:
	case <-time.After(time.Second):
		t.Fatal("second lock set did not enter after shared key released")
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second lock set failed: %v", err)
	}
}
