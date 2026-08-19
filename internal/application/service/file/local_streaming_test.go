package file

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type readerWithTerminalError struct {
	data   []byte
	offset int
	err    error
}

func (r *readerWithTerminalError) Read(p []byte) (int, error) {
	if r.offset == len(r.data) {
		return 0, r.err
	}

	n := copy(p, r.data[r.offset:])
	r.offset += n
	return n, nil
}

func requireDirectoryEmpty(t *testing.T, dir string) {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestLocalFileServiceSaveReaderStoresCompleteContent(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()
	service := &localFileService{baseDir: baseDir}
	content := []byte("complete streaming content")

	storedPath, err := service.SaveReader(
		context.Background(),
		bytes.NewReader(content),
		int64(len(content)),
		"document.txt",
		"text/plain",
		42,
		"knowledge-id",
	)

	require.NoError(t, err)
	require.True(t, strings.HasPrefix(storedPath, localScheme))
	relativePath := strings.TrimPrefix(storedPath, localScheme)
	storedContent, err := os.ReadFile(filepath.Join(baseDir, filepath.FromSlash(relativePath)))
	require.NoError(t, err)
	require.Equal(t, content, storedContent)
}

func TestLocalFileServiceSaveReaderRemovesFileWhenContextCanceled(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()
	service := &localFileService{baseDir: baseDir}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	storedPath, err := service.SaveReader(
		ctx,
		strings.NewReader("must not remain"),
		int64(len("must not remain")),
		"document.txt",
		"text/plain",
		42,
		"knowledge-id",
	)

	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, storedPath)
	requireDirectoryEmpty(t, filepath.Join(baseDir, "42", "knowledge-id"))
}

func TestLocalFileServiceSaveReaderRemovesFileWhenReaderFails(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()
	service := &localFileService{baseDir: baseDir}
	readerErr := errors.New("reader failed")
	reader := &readerWithTerminalError{
		data: []byte("partial content"),
		err:  readerErr,
	}

	storedPath, err := service.SaveReader(
		context.Background(),
		reader,
		int64(len(reader.data)),
		"document.txt",
		"text/plain",
		42,
		"knowledge-id",
	)

	require.ErrorIs(t, err, readerErr)
	require.Empty(t, storedPath)
	requireDirectoryEmpty(t, filepath.Join(baseDir, "42", "knowledge-id"))
}

var _ io.Reader = (*readerWithTerminalError)(nil)
