package interfaces

import (
	"context"
	"io"
	"mime/multipart"
)

// FileService is the interface for file services.
// FileService provides methods to save, retrieve, and delete files.
type FileService interface {
	// CheckConnectivity verifies that the storage backend is reachable and
	// properly configured (e.g. bucket exists, credentials valid).
	CheckConnectivity(ctx context.Context) error
	// SaveFile saves a file.
	SaveFile(ctx context.Context, file *multipart.FileHeader, tenantID uint64, knowledgeID string) (string, error)
	// SaveBytes saves bytes data to a file and returns the file path.
	// If temp is true, the file will be saved to a temporary storage that may auto-expire.
	SaveBytes(ctx context.Context, data []byte, tenantID uint64, fileName string, temp bool) (string, error)
	// GetFile retrieves a file.
	GetFile(ctx context.Context, filePath string) (io.ReadCloser, error)
	// GetFileURL returns a download URL for the file (if supported by the storage backend).
	GetFileURL(ctx context.Context, filePath string) (string, error)
	// DeleteFile deletes a file.
	DeleteFile(ctx context.Context, filePath string) error
	// CopyFile copies an existing stored object to a NEW object owned by
	// (tenantID, knowledgeID), returning the new provider:// path. The copy is
	// independent: deleting the source never affects it. Returns ErrCrossBackendCopy
	// when srcPath belongs to a different storage provider than this service.
	CopyFile(ctx context.Context, srcPath string, tenantID uint64, knowledgeID string) (string, error)
}

// StreamingFileService 直接从读取器保存文件，无需构造 multipart.FileHeader。
// 可续传上传通过该接口完成大文件落盘，避免把整个文件加载到内存。
type StreamingFileService interface {
	SaveReader(ctx context.Context, reader io.Reader, size int64, fileName, contentType string, tenantID uint64, knowledgeID string) (string, error)
}

// PreparedStreamingFileService 先生成稳定存储路径，再向该路径流式写入。
// 可续传上传先持久化路径，进程崩溃后才能定位并继续或清理最终对象。
type PreparedStreamingFileService interface {
	StreamingFileService
	PrepareReaderPath(ctx context.Context, size int64, fileName, contentType string, tenantID uint64, knowledgeID string) (string, error)
	SaveReaderTo(ctx context.Context, reader io.Reader, size int64, fileName, contentType string, tenantID uint64, knowledgeID, filePath string) error
	FinalizeReaderPath(ctx context.Context, size int64, fileName, contentType string, tenantID uint64, knowledgeID, filePath string) (string, error)
}
