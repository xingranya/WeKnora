DROP TABLE IF EXISTS knowledge_upload_parts;
DROP TABLE IF EXISTS knowledge_upload_sessions;
DROP INDEX IF EXISTS idx_knowledge_folders_kb;
DROP INDEX IF EXISTS uniq_knowledge_folders_active_path;
DROP TABLE IF EXISTS knowledge_folders;
ALTER TABLE knowledges DROP COLUMN IF EXISTS source_file_quota_bytes;
