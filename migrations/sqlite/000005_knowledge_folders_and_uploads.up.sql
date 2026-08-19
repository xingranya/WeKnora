-- Lite 模式的持久化目录、可续传上传和原始文件配额字段。
ALTER TABLE knowledges ADD COLUMN source_file_quota_bytes BIGINT NOT NULL DEFAULT 0;

CREATE TABLE IF NOT EXISTS knowledge_folders (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL,
    path VARCHAR(1024) NOT NULL,
    created_by VARCHAR(512) NOT NULL DEFAULT '',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    deleted_at DATETIME
);

CREATE UNIQUE INDEX IF NOT EXISTS uniq_knowledge_folders_active_path
    ON knowledge_folders (tenant_id, knowledge_base_id, path)
    WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_knowledge_folders_kb
    ON knowledge_folders (tenant_id, knowledge_base_id, deleted_at);

WITH RECURSIVE folder_ancestors(tenant_id, knowledge_base_id, path, remaining) AS (
    SELECT tenant_id, knowledge_base_id, '', folder_path || '/'
    FROM knowledges
    WHERE deleted_at IS NULL AND folder_path <> ''
    UNION ALL
    SELECT tenant_id,
           knowledge_base_id,
           CASE
               WHEN path = '' THEN substr(remaining, 1, instr(remaining, '/') - 1)
               ELSE path || '/' || substr(remaining, 1, instr(remaining, '/') - 1)
           END,
           substr(remaining, instr(remaining, '/') + 1)
    FROM folder_ancestors
    WHERE remaining <> ''
)
INSERT INTO knowledge_folders (id, tenant_id, knowledge_base_id, path)
SELECT lower(hex(randomblob(16))), tenant_id, knowledge_base_id, path
FROM folder_ancestors
WHERE path <> ''
ON CONFLICT DO NOTHING;

CREATE TABLE IF NOT EXISTS knowledge_upload_sessions (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL,
    user_id VARCHAR(512) NOT NULL,
    file_name VARCHAR(1024) NOT NULL,
    file_size BIGINT NOT NULL,
    mime_type VARCHAR(255) NOT NULL DEFAULT 'application/octet-stream',
    last_modified BIGINT NOT NULL DEFAULT 0,
    folder_path VARCHAR(1024) NOT NULL DEFAULT '',
    chunk_size BIGINT NOT NULL,
    received_bytes BIGINT NOT NULL DEFAULT 0,
    status VARCHAR(32) NOT NULL DEFAULT 'created',
    temp_path TEXT NOT NULL,
    final_file_path TEXT NOT NULL DEFAULT '',
    finalize_stage VARCHAR(32) NOT NULL DEFAULT '',
    options TEXT NOT NULL DEFAULT '{}',
    knowledge_id VARCHAR(36) NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    expires_at DATETIME NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_upload_sessions_owner
    ON knowledge_upload_sessions (tenant_id, user_id, status, expires_at);
CREATE INDEX IF NOT EXISTS idx_upload_sessions_kb
    ON knowledge_upload_sessions (tenant_id, knowledge_base_id, status);
CREATE INDEX IF NOT EXISTS idx_upload_sessions_expiry
    ON knowledge_upload_sessions (expires_at, status);

CREATE TABLE IF NOT EXISTS knowledge_upload_parts (
    session_id VARCHAR(36) NOT NULL,
    part_number INTEGER NOT NULL,
    part_offset BIGINT NOT NULL,
    part_size BIGINT NOT NULL,
    sha256 VARCHAR(64) NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (session_id, part_number)
);

CREATE INDEX IF NOT EXISTS idx_upload_parts_session
    ON knowledge_upload_parts (session_id, part_offset);
