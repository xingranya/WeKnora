DO $$ BEGIN RAISE NOTICE '[Migration 000087] Removing authentication token fingerprints...'; END $$;

-- token 明文列在 up 迁移中始终保留，因此删除指纹列后旧镜像仍可验证、刷新和撤销现有会话。
DROP INDEX IF EXISTS idx_auth_tokens_token_fingerprint;

ALTER TABLE auth_tokens
    DROP COLUMN IF EXISTS token_fingerprint;

COMMENT ON COLUMN auth_tokens.token IS 'Token value (JWT or other format)';
