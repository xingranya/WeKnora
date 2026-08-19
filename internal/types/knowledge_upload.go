package types

import (
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrUploadPartConflict   = errors.New("upload part conflicts with confirmed part")
	ErrUploadPartOutOfOrder = errors.New("upload part offset is not the next confirmed offset")
	ErrUploadSessionState   = errors.New("upload session is not writable")
	ErrUploadIncomplete     = errors.New("upload session has incomplete parts")
	ErrUploadUserQuota      = errors.New("upload session user quota exceeded")
	ErrUploadTenantQuota    = errors.New("upload session tenant quota exceeded")
)

// KnowledgeUploadStatus 表示可续传上传会话的生命周期状态。
type KnowledgeUploadStatus string

// KnowledgeUploadFinalizeStage 记录最终文件和知识记录的持久化进度。
type KnowledgeUploadFinalizeStage string

const (
	KnowledgeUploadCreated                 KnowledgeUploadStatus = "created"
	KnowledgeUploadUploading               KnowledgeUploadStatus = "uploading"
	KnowledgeUploadCompleting              KnowledgeUploadStatus = "completing"
	KnowledgeUploadCompletedCleanupPending KnowledgeUploadStatus = "completed_cleanup_pending"
	KnowledgeUploadCancelledCleanupPending KnowledgeUploadStatus = "cancelled_cleanup_pending"
	KnowledgeUploadExpiredCleanupPending   KnowledgeUploadStatus = "expired_cleanup_pending"
	KnowledgeUploadCompleted               KnowledgeUploadStatus = "completed"
	KnowledgeUploadFailed                  KnowledgeUploadStatus = "failed"
	KnowledgeUploadCancelled               KnowledgeUploadStatus = "cancelled"
	KnowledgeUploadExpired                 KnowledgeUploadStatus = "expired"

	KnowledgeUploadFinalizeNone             KnowledgeUploadFinalizeStage = ""
	KnowledgeUploadFinalizeFilePrepared     KnowledgeUploadFinalizeStage = "file_prepared"
	KnowledgeUploadFinalizeFileStored       KnowledgeUploadFinalizeStage = "file_stored"
	KnowledgeUploadFinalizeKnowledgeCreated KnowledgeUploadFinalizeStage = "knowledge_created"
)

func (s KnowledgeUploadStatus) Terminal() bool {
	return s == KnowledgeUploadCompleted || s == KnowledgeUploadCancelled || s == KnowledgeUploadExpired || s.CleanupPending()
}

func (s KnowledgeUploadStatus) CleanupPending() bool {
	return s == KnowledgeUploadCompletedCleanupPending ||
		s == KnowledgeUploadCancelledCleanupPending ||
		s == KnowledgeUploadExpiredCleanupPending
}

func (s KnowledgeUploadStatus) CleanupFinalStatus() (KnowledgeUploadStatus, bool) {
	switch s {
	case KnowledgeUploadCompletedCleanupPending:
		return KnowledgeUploadCompleted, true
	case KnowledgeUploadCancelledCleanupPending:
		return KnowledgeUploadCancelled, true
	case KnowledgeUploadExpiredCleanupPending:
		return KnowledgeUploadExpired, true
	default:
		return "", false
	}
}

// KnowledgeUploadOptions 保存完成上传后创建知识所需的配置快照。
type KnowledgeUploadOptions struct {
	Metadata         map[string]string          `json:"metadata,omitempty"`
	TagIDs           []string                   `json:"tag_ids,omitempty"`
	Channel          string                     `json:"channel,omitempty"`
	EnableMultimodel *bool                      `json:"enable_multimodel,omitempty"`
	ProcessConfig    *KnowledgeProcessOverrides `json:"process_config,omitempty"`
}

// KnowledgeUploadSession 表示一个持久化可续传上传会话。
type KnowledgeUploadSession struct {
	ID                 string                       `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID           uint64                       `json:"tenant_id" gorm:"not null;index"`
	KnowledgeBaseID    string                       `json:"knowledge_base_id" gorm:"type:varchar(36);not null;index"`
	UserID             string                       `json:"user_id" gorm:"type:varchar(512);not null;index"`
	FileName           string                       `json:"file_name" gorm:"type:varchar(1024);not null"`
	FileSize           int64                        `json:"file_size" gorm:"not null"`
	MIMEType           string                       `json:"mime_type" gorm:"type:varchar(255);not null"`
	LastModified       int64                        `json:"last_modified"`
	FolderPath         string                       `json:"folder_path" gorm:"type:varchar(1024);not null"`
	ChunkSize          int64                        `json:"chunk_size" gorm:"not null"`
	ReceivedBytes      int64                        `json:"received_bytes" gorm:"not null"`
	Status             KnowledgeUploadStatus        `json:"status" gorm:"type:varchar(32);not null"`
	TempPath           string                       `json:"-" gorm:"type:text;not null"`
	FinalFilePath      string                       `json:"-" gorm:"type:text;not null"`
	FinalizeStage      KnowledgeUploadFinalizeStage `json:"-" gorm:"type:varchar(32);not null"`
	Options            JSON                         `json:"-" gorm:"type:jsonb;not null"`
	KnowledgeID        string                       `json:"knowledge_id,omitempty" gorm:"type:varchar(36);not null"`
	ErrorMessage       string                       `json:"error_message,omitempty" gorm:"type:text;not null"`
	ExpiresAt          time.Time                    `json:"expires_at" gorm:"index"`
	CreatedAt          time.Time                    `json:"created_at"`
	UpdatedAt          time.Time                    `json:"updated_at"`
	ReceivedParts      []int                        `json:"received_parts,omitempty" gorm:"-"`
	ReceivedPartHashes map[int]string               `json:"received_part_hashes,omitempty" gorm:"-"`
}

func (KnowledgeUploadSession) TableName() string { return "knowledge_upload_sessions" }

func (s *KnowledgeUploadSession) ParsedOptions() (*KnowledgeUploadOptions, error) {
	if s == nil || len(s.Options) == 0 {
		return &KnowledgeUploadOptions{}, nil
	}
	var options KnowledgeUploadOptions
	if err := json.Unmarshal(s.Options, &options); err != nil {
		return nil, err
	}
	return &options, nil
}

// KnowledgeUploadPart 表示服务端已校验并确认的文件分片。
type KnowledgeUploadPart struct {
	SessionID  string    `json:"session_id" gorm:"type:varchar(36);primaryKey"`
	PartNumber int       `json:"part_number" gorm:"primaryKey"`
	PartOffset int64     `json:"part_offset" gorm:"not null"`
	PartSize   int64     `json:"part_size" gorm:"not null"`
	SHA256     string    `json:"sha256" gorm:"type:varchar(64);not null"`
	CreatedAt  time.Time `json:"created_at"`
}

func (KnowledgeUploadPart) TableName() string { return "knowledge_upload_parts" }

// KnowledgeUploadInit 包含初始化上传会话所需的文件元数据。
type KnowledgeUploadInit struct {
	FileName     string
	FileSize     int64
	MIMEType     string
	LastModified int64
	FolderPath   string
	Options      KnowledgeUploadOptions
}
