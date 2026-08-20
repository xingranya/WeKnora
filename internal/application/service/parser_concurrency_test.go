package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/infrastructure/docparser"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

type signalingTemporaryDocumentReader struct {
	entered chan context.Context
}

func (r *signalingTemporaryDocumentReader) Read(
	ctx context.Context,
	_ *types.ReadRequest,
) (*types.ReadResult, error) {
	r.entered <- ctx
	return &types.ReadResult{MarkdownContent: "parsed"}, nil
}

func TestTemporaryDocumentReadSharesMinerUSlotWithKnowledge(t *testing.T) {
	docparser.ConfigureConcurrency(nil)
	t.Cleanup(func() { docparser.ConfigureConcurrency(nil) })
	t.Setenv("WEKNORA_MINERU_MAX_CONCURRENCY", "")
	overrides := map[string]string{"mineru_max_concurrency": "1"}

	_, releaseKnowledge, err := gateParserRead(
		context.Background(),
		docparser.MinerUEngineName,
		overrides,
	)
	require.NoError(t, err)
	knowledgeReleased := false
	t.Cleanup(func() {
		if !knowledgeReleased {
			releaseKnowledge()
		}
	})

	type readResult struct {
		result *types.ReadResult
		err    error
	}
	reader := &signalingTemporaryDocumentReader{entered: make(chan context.Context, 1)}
	attachmentFinished := make(chan readResult, 1)
	waitCtx, cancelWait := context.WithCancel(context.Background())
	waiterDone := make(chan struct{})
	t.Cleanup(func() {
		cancelWait()
		select {
		case <-waiterDone:
		case <-time.After(time.Second):
			t.Error("临时附件并发测试的等待协程未退出")
		}
	})
	go func() {
		defer close(waiterDone)
		result, readErr := readTemporaryDocumentWithParserGate(
			waitCtx,
			docparser.MinerUEngineName,
			overrides,
			reader,
			&types.ReadRequest{FileName: "attachment.pdf"},
		)
		attachmentFinished <- readResult{result: result, err: readErr}
	}()

	select {
	case <-reader.entered:
		t.Fatal("临时附件 reader 不应绕过知识库任务占用的 MinerU 槽位")
	case <-time.After(100 * time.Millisecond):
	}

	releaseKnowledge()
	knowledgeReleased = true
	select {
	case leaseCtx := <-reader.entered:
		require.NotNil(t, leaseCtx)
	case <-time.After(time.Second):
		t.Fatal("知识库任务释放后，临时附件 reader 应进入 MinerU")
	}
	select {
	case result := <-attachmentFinished:
		require.NoError(t, result.err)
		require.Equal(t, "parsed", result.result.MarkdownContent)
	case <-time.After(time.Second):
		t.Fatal("临时附件 reader 返回后应释放 MinerU 槽位")
	}
}

func TestTemporaryDocumentReadCancelsWhileWaitingForMinerU(t *testing.T) {
	docparser.ConfigureConcurrency(nil)
	t.Cleanup(func() { docparser.ConfigureConcurrency(nil) })
	t.Setenv("WEKNORA_MINERU_MAX_CONCURRENCY", "")
	overrides := map[string]string{"mineru_max_concurrency": "1"}

	_, releaseKnowledge, err := gateParserRead(
		context.Background(),
		docparser.MinerUEngineName,
		overrides,
	)
	require.NoError(t, err)
	defer releaseKnowledge()

	ctx, cancel := context.WithCancel(context.Background())
	reader := &signalingTemporaryDocumentReader{entered: make(chan context.Context, 1)}
	finished := make(chan error, 1)
	go func() {
		_, readErr := readTemporaryDocumentWithParserGate(
			ctx,
			docparser.MinerUEngineName,
			overrides,
			reader,
			&types.ReadRequest{FileName: "cancelled.pdf"},
		)
		finished <- readErr
	}()

	cancel()
	select {
	case readErr := <-finished:
		require.Error(t, readErr)
		require.True(t, errors.Is(readErr, context.Canceled))
	case <-time.After(time.Second):
		t.Fatal("取消等待后临时附件必须及时返回")
	}
	select {
	case <-reader.entered:
		t.Fatal("取消等待后不应进入临时附件 reader")
	default:
	}
}

func TestTemporaryDocumentReadDoesNotGateOtherParsers(t *testing.T) {
	docparser.ConfigureConcurrency(nil)
	t.Cleanup(func() { docparser.ConfigureConcurrency(nil) })
	t.Setenv("DOCREADER_GRPC_MAX_WORKERS", "1")
	t.Setenv("WEKNORA_DOCREADER_MAX_CONCURRENCY", "1")
	_, releaseKnowledge, err := gateParserReadForRoute(
		context.Background(),
		docparser.BuiltinEngineName,
		"pdf",
		false,
		nil,
	)
	require.NoError(t, err)
	defer releaseKnowledge()

	reader := &signalingTemporaryDocumentReader{entered: make(chan context.Context, 1)}
	leaseCtx, release, err := gateParserReadForRoute(
		context.Background(),
		docparser.SimpleEngineName,
		"txt",
		false,
		nil,
	)
	require.NoError(t, err)
	release()
	result, err := reader.Read(leaseCtx, &types.ReadRequest{FileName: "document.txt"})
	require.NoError(t, err)
	require.Equal(t, "parsed", result.MarkdownContent)
}

func TestParserConcurrencyLimitUsesEnvironmentOverride(t *testing.T) {
	t.Setenv("WEKNORA_MINERU_MAX_CONCURRENCY", "3")
	require.Equal(t, 3, parserConcurrencyLimit(map[string]string{
		"mineru_max_concurrency": "2",
	}))
}

func TestDocReaderConcurrencyLimitNeverExceedsWorkerCount(t *testing.T) {
	t.Setenv("DOCREADER_GRPC_MAX_WORKERS", "4")
	t.Setenv("WEKNORA_DOCREADER_MAX_CONCURRENCY", "10")
	require.Equal(t, 4, docReaderConcurrencyLimit())

	t.Setenv("WEKNORA_DOCREADER_MAX_CONCURRENCY", "2")
	require.Equal(t, 2, docReaderConcurrencyLimit())
}

func TestRemoteDocReaderRoutesShareWorkerGate(t *testing.T) {
	docparser.ConfigureConcurrency(nil)
	t.Cleanup(func() { docparser.ConfigureConcurrency(nil) })
	t.Setenv("DOCREADER_GRPC_MAX_WORKERS", "1")
	t.Setenv("WEKNORA_DOCREADER_MAX_CONCURRENCY", "1")

	_, releaseFirst, err := gateParserReadForRoute(
		context.Background(),
		docparser.BuiltinEngineName,
		"pdf",
		false,
		nil,
	)
	require.NoError(t, err)
	firstReleased := false
	t.Cleanup(func() {
		if !firstReleased {
			releaseFirst()
		}
	})

	acquired := make(chan func(), 1)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		_, release, acquireErr := gateParserReadForRoute(
			ctx,
			"remote-only",
			"pdf",
			false,
			nil,
		)
		if acquireErr != nil {
			acquired <- nil
			return
		}
		acquired <- release
	}()

	select {
	case <-acquired:
		t.Fatal("远端解析任务不应绕过 DocReader worker 闸门")
	case <-time.After(100 * time.Millisecond):
	}

	releaseFirst()
	firstReleased = true
	select {
	case release := <-acquired:
		require.NotNil(t, release)
		release()
	case <-time.After(time.Second):
		t.Fatal("DocReader 槽位释放后，等待任务应继续执行")
	}
}
