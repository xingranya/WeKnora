-- 持久化知识库空文件夹和可续传上传会话。
--
-- 历史 folder_path 由旧上传路径直接回填。Go 侧规范化还包含 Unicode
-- TrimSpace 等规则，无法在纯 SQL 中无损等价复刻，因此本迁移不改写
-- knowledges.folder_path。先对可确定的结构边界做只读预检；发现异常时在
-- 创建任何新表之前中止，由部署人员备份并通过应用层审计历史数据。
DO $$
DECLARE
    unsafe_path_count BIGINT;
BEGIN
    SELECT COUNT(*)
    INTO unsafe_path_count
    FROM knowledges AS knowledge
    WHERE knowledge.deleted_at IS NULL
      AND knowledge.folder_path <> ''
      AND (
          knowledge.folder_path LIKE '/%'
          OR knowledge.folder_path LIKE '%/'
          OR knowledge.folder_path LIKE '%//%'
          OR POSITION(E'\\' IN knowledge.folder_path) > 0
          OR OCTET_LENGTH(knowledge.folder_path) > 1024
          OR CARDINALITY(regexp_split_to_array(knowledge.folder_path, '/')) > 16
          OR EXISTS (
              SELECT 1
              FROM unnest(regexp_split_to_array(knowledge.folder_path, '/')) AS part(segment)
              WHERE part.segment = ''
                 OR part.segment IN ('.', '..')
                 OR part.segment <> btrim(part.segment, E' \t\n\r\f' || chr(11))
                 OR left(part.segment, 1) IN (
                     chr(133), chr(160), chr(5760), chr(8192), chr(8193), chr(8194),
                     chr(8195), chr(8196), chr(8197), chr(8198), chr(8199), chr(8200),
                     chr(8201), chr(8202), chr(8232), chr(8233), chr(8239), chr(8287), chr(12288)
                 )
                 OR right(part.segment, 1) IN (
                     chr(133), chr(160), chr(5760), chr(8192), chr(8193), chr(8194),
                     chr(8195), chr(8196), chr(8197), chr(8198), chr(8199), chr(8200),
                     chr(8201), chr(8202), chr(8232), chr(8233), chr(8239), chr(8287), chr(12288)
                 )
                 OR part.segment <> rtrim(part.segment, '. ')
                 OR OCTET_LENGTH(part.segment) > 128
          )
      );

    IF unsafe_path_count > 0 THEN
        RAISE EXCEPTION
            '迁移 000086 已停止：发现 % 条历史 folder_path 需要应用层规范化',
            unsafe_path_count
            USING HINT = '请先备份并审计这些历史路径；本迁移不会改写 knowledges.folder_path。';
    END IF;
END $$;

ALTER TABLE knowledges
    ADD COLUMN IF NOT EXISTS source_file_quota_bytes BIGINT NOT NULL DEFAULT 0;

CREATE TABLE IF NOT EXISTS knowledge_folders (
    id                VARCHAR(36) PRIMARY KEY,
    tenant_id         BIGINT NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL,
    path              VARCHAR(1024) NOT NULL,
    created_by        VARCHAR(36) NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at        TIMESTAMPTZ
);

CREATE UNIQUE INDEX IF NOT EXISTS uniq_knowledge_folders_active_path
    ON knowledge_folders (tenant_id, knowledge_base_id, path)
    WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_knowledge_folders_kb
    ON knowledge_folders (tenant_id, knowledge_base_id, deleted_at);

-- 预检通过后，按原值回填已有文件夹及其全部祖先目录，不改写历史知识路径。
WITH source_paths AS (
    SELECT DISTINCT tenant_id, knowledge_base_id,
           regexp_split_to_array(folder_path, '/') AS parts
    FROM knowledges
    WHERE deleted_at IS NULL AND folder_path <> ''
), ancestors AS (
    SELECT tenant_id, knowledge_base_id,
           array_to_string(parts[1:depth], '/') AS path
    FROM source_paths
    CROSS JOIN LATERAL generate_series(1, array_length(parts, 1)) AS depth
)
INSERT INTO knowledge_folders (id, tenant_id, knowledge_base_id, path)
SELECT gen_random_uuid()::text, tenant_id, knowledge_base_id, path
FROM ancestors
WHERE path <> ''
ON CONFLICT DO NOTHING;

CREATE TABLE IF NOT EXISTS knowledge_upload_sessions (
    id                   VARCHAR(36) PRIMARY KEY,
    tenant_id            BIGINT NOT NULL,
    knowledge_base_id    VARCHAR(36) NOT NULL,
    user_id              VARCHAR(512) NOT NULL,
    file_name            VARCHAR(1024) NOT NULL,
    file_size            BIGINT NOT NULL,
    mime_type            VARCHAR(255) NOT NULL DEFAULT 'application/octet-stream',
    last_modified        BIGINT NOT NULL DEFAULT 0,
    folder_path          VARCHAR(1024) NOT NULL DEFAULT '',
    chunk_size           BIGINT NOT NULL,
    received_bytes       BIGINT NOT NULL DEFAULT 0,
    status               VARCHAR(32) NOT NULL DEFAULT 'created',
    temp_path            TEXT NOT NULL,
    final_file_path      TEXT NOT NULL DEFAULT '',
    finalize_stage       VARCHAR(32) NOT NULL DEFAULT '',
    options              JSONB NOT NULL DEFAULT '{}'::jsonb,
    knowledge_id         VARCHAR(36) NOT NULL DEFAULT '',
    error_message        TEXT NOT NULL DEFAULT '',
    expires_at           TIMESTAMPTZ NOT NULL,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_upload_sessions_owner
    ON knowledge_upload_sessions (tenant_id, user_id, status, expires_at);
CREATE INDEX IF NOT EXISTS idx_upload_sessions_kb
    ON knowledge_upload_sessions (tenant_id, knowledge_base_id, status);
CREATE INDEX IF NOT EXISTS idx_upload_sessions_expiry
    ON knowledge_upload_sessions (expires_at)
    WHERE status IN (
        'created', 'uploading', 'completing', 'failed',
        'completed_cleanup_pending', 'cancelled_cleanup_pending', 'expired_cleanup_pending'
    );

CREATE TABLE IF NOT EXISTS knowledge_upload_parts (
    session_id   VARCHAR(36) NOT NULL,
    part_number  INT NOT NULL,
    part_offset  BIGINT NOT NULL,
    part_size    BIGINT NOT NULL,
    sha256       VARCHAR(64) NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (session_id, part_number)
);

CREATE INDEX IF NOT EXISTS idx_upload_parts_session
    ON knowledge_upload_parts (session_id, part_offset);
