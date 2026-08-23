package container

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

type startupDocumentReaderStub struct{}

func (startupDocumentReaderStub) Read(context.Context, *types.ReadRequest) (*types.ReadResult, error) {
	return &types.ReadResult{}, nil
}

func (startupDocumentReaderStub) Reconnect(string) error { return nil }

func (startupDocumentReaderStub) IsConnected() bool { return true }

func (startupDocumentReaderStub) ListEngines(context.Context, map[string]string) ([]types.ParserEngineInfo, error) {
	return nil, nil
}

func TestConnectDocumentReaderWithRetryRecoversTransientStartupFailure(t *testing.T) {
	attempts := 0
	waits := 0
	reader, err := connectDocumentReaderWithRetry(
		"docreader:50051",
		3,
		time.Millisecond,
		func(string) (interfaces.DocumentReader, error) {
			attempts++
			if attempts < 3 {
				return nil, errors.New("docreader 尚未就绪")
			}
			return startupDocumentReaderStub{}, nil
		},
		func(time.Duration) { waits++ },
	)
	if err != nil {
		t.Fatalf("connectDocumentReaderWithRetry() error = %v", err)
	}
	if reader == nil {
		t.Fatal("connectDocumentReaderWithRetry() reader = nil")
	}
	if attempts != 3 || waits != 2 {
		t.Fatalf("attempts/waits = %d/%d, want 3/2", attempts, waits)
	}
}

func TestConnectDocumentReaderWithRetryReturnsLastError(t *testing.T) {
	attempts := 0
	waits := 0
	reader, err := connectDocumentReaderWithRetry(
		"docreader:50051",
		2,
		time.Millisecond,
		func(string) (interfaces.DocumentReader, error) {
			attempts++
			return nil, errors.New("连接失败")
		},
		func(time.Duration) { waits++ },
	)
	if reader != nil {
		t.Fatalf("connectDocumentReaderWithRetry() reader = %#v, want nil", reader)
	}
	if err == nil || !strings.Contains(err.Error(), "after 2 attempts") || !strings.Contains(err.Error(), "连接失败") {
		t.Fatalf("connectDocumentReaderWithRetry() error = %v", err)
	}
	if attempts != 2 || waits != 1 {
		t.Fatalf("attempts/waits = %d/%d, want 2/1", attempts, waits)
	}
}

func TestBoundedPositiveEnvIntRejectsUnsafeValues(t *testing.T) {
	t.Setenv("DOCREADER_TEST_RETRIES", "0")
	if got := boundedPositiveEnvInt("DOCREADER_TEST_RETRIES", 6, 1, 60); got != 6 {
		t.Fatalf("zero value = %d, want fallback 6", got)
	}
	t.Setenv("DOCREADER_TEST_RETRIES", "61")
	if got := boundedPositiveEnvInt("DOCREADER_TEST_RETRIES", 6, 1, 60); got != 6 {
		t.Fatalf("oversized value = %d, want fallback 6", got)
	}
	t.Setenv("DOCREADER_TEST_RETRIES", "12")
	if got := boundedPositiveEnvInt("DOCREADER_TEST_RETRIES", 6, 1, 60); got != 12 {
		t.Fatalf("valid value = %d, want 12", got)
	}
}
