package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Tencent/WeKnora/internal/infrastructure/docparser"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestParserUploadMaxBytesUsesDocReaderPayloadLimit(t *testing.T) {
	t.Setenv("DOCREADER_GRPC_MAX_FILE_SIZE_MB", "50")
	t.Setenv("MAX_FILE_SIZE_MB", "50")
	t.Setenv("KNOWLEDGE_UPLOAD_MAX_FILE_SIZE_MB", "2048")
	want := int64(49 * 1024 * 1024)

	for _, engine := range []string{"", docparser.BuiltinEngineName, "remote-only"} {
		if got := parserUploadMaxBytes(engine); got != want {
			t.Errorf("parserUploadMaxBytes(%q) = %d, want %d", engine, got, want)
		}
	}
}

func TestKnowledgeUploadFinalizeConcurrencyDefaultsAndOverrides(t *testing.T) {
	t.Setenv("KNOWLEDGE_UPLOAD_FINALIZE_MAX_CONCURRENCY", "")
	if got := knowledgeUploadFinalizeConcurrency(); got != 1 {
		t.Fatalf("default finalization concurrency = %d, want 1", got)
	}
	t.Setenv("KNOWLEDGE_UPLOAD_FINALIZE_MAX_CONCURRENCY", "3")
	if got := knowledgeUploadFinalizeConcurrency(); got != 3 {
		t.Fatalf("configured finalization concurrency = %d, want 3", got)
	}
}

func TestCalculatePathContentHashesUsesSHA256AndLegacyMD5(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "upload-hash-*.bin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	hashes, err := calculatePathContentHashes(context.Background(), file.Name())
	if err != nil {
		t.Fatal(err)
	}
	if hashes.SHA256 != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("SHA256 = %q", hashes.SHA256)
	}
	if hashes.LegacyMD5 != "900150983cd24fb0d6963f7d28e17f72" {
		t.Fatalf("legacy MD5 = %q", hashes.LegacyMD5)
	}
}

func TestCalculatePathContentHashesHonorsCancelledContext(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "upload-hash-*.bin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(make([]byte, 2*1024*1024)); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = calculatePathContentHashes(ctx, file.Name())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("calculatePathContentHashes() error = %v, want context.Canceled", err)
	}
}

func TestParserUploadMaxBytesKeepsGlobalUploadCeiling(t *testing.T) {
	t.Setenv("KNOWLEDGE_UPLOAD_MAX_FILE_SIZE_MB", "512")
	want := int64(512 * 1024 * 1024)
	if got := parserUploadMaxBytes(docparser.MinerUEngineName); got != want {
		t.Fatalf("parserUploadMaxBytes(mineru) = %d, want %d", got, want)
	}
}

func TestEnsureKnowledgeUploadRootIdentityPublishesOneCompleteValue(t *testing.T) {
	tempRoot := t.TempDir()
	const workers = 16
	identities := make(chan string, workers)
	errorsSeen := make(chan error, workers)
	var group sync.WaitGroup
	for index := 0; index < workers; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			identity, err := ensureKnowledgeUploadRootIdentity(tempRoot)
			if err != nil {
				errorsSeen <- err
				return
			}
			identities <- identity
		}()
	}
	group.Wait()
	close(identities)
	close(errorsSeen)
	for err := range errorsSeen {
		t.Fatalf("ensureKnowledgeUploadRootIdentity() error: %v", err)
	}

	var expected string
	for identity := range identities {
		if expected == "" {
			expected = identity
		}
		if identity != expected {
			t.Fatalf("concurrent identities differ: %q != %q", identity, expected)
		}
	}
	content, err := os.ReadFile(filepath.Join(tempRoot, knowledgeUploadRootIdentityFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != expected {
		t.Fatalf("published identity = %q, want %q", string(content), expected)
	}
}

func TestValidateKnowledgeUploadTempStorageChecksDefaultDirectoryAcrossReplicas(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	t.Setenv("KNOWLEDGE_UPLOAD_TEMP_DIR", "")

	firstRoot := t.TempDir()
	t.Setenv("LOCAL_STORAGE_BASE_DIR", firstRoot)
	if err := validateKnowledgeUploadTempStorage(context.Background(), client); err != nil {
		t.Fatalf("first replica validation failed: %v", err)
	}

	secondRoot := t.TempDir()
	t.Setenv("LOCAL_STORAGE_BASE_DIR", secondRoot)
	err := validateKnowledgeUploadTempStorage(context.Background(), client)
	if err == nil || !strings.Contains(err.Error(), "not shared across app replicas") {
		t.Fatalf("second replica validation error = %v, want shared-root rejection", err)
	}
}
