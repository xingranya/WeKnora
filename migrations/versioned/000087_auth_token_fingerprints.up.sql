DO $$ BEGIN RAISE NOTICE '[Migration 000087] Adding authentication token fingerprints...'; END $$;

-- 保留 token 明文列供发布前旧镜像继续查询，新增独立指纹列供新镜像认证。
-- 这样迁移期间可以双读/双写，回滚旧镜像时现有会话不会因数据不可逆而失效。
ALTER TABLE auth_tokens
    ADD COLUMN IF NOT EXISTS token_fingerprint VARCHAR(64);

-- 历史实现的 JWT 缺少 jti，同一用户在同一秒并发签发时可能产生同值行。
-- 相同原始 token 代表同一凭据，保留最新有效行并撤销其余行不会使用户掉线，
-- 同时为“一个 refresh token 只可消费一次”建立确定语义。
WITH ranked_active_tokens AS (
    SELECT id,
           row_number() OVER (
               PARTITION BY token
               ORDER BY expires_at DESC, created_at DESC, id DESC
           ) AS duplicate_rank
    FROM auth_tokens
    WHERE is_revoked = FALSE
)
UPDATE auth_tokens AS auth_token
SET is_revoked = TRUE,
    updated_at = NOW()
FROM ranked_active_tokens AS ranked
WHERE auth_token.id = ranked.id
  AND ranked.duplicate_rank > 1;

-- PostgreSQL 内置 sha256(bytea)，无需额外扩展。IS DISTINCT FROM 同时覆盖
-- NULL、空值和错误指纹，使迁移在 dirty 恢复或隔离环境重复执行时仍可收敛。
UPDATE auth_tokens
SET token_fingerprint = encode(sha256(convert_to(token, 'UTF8')), 'hex')
WHERE token_fingerprint IS DISTINCT FROM encode(sha256(convert_to(token, 'UTF8')), 'hex');

DROP INDEX IF EXISTS idx_auth_tokens_token_fingerprint;
CREATE UNIQUE INDEX idx_auth_tokens_token_fingerprint
    ON auth_tokens (token_fingerprint)
    WHERE token_fingerprint IS NOT NULL AND is_revoked = FALSE;

COMMENT ON COLUMN auth_tokens.token IS '原始访问或刷新令牌；兼容回滚窗口内的旧镜像，禁止通过 API 返回';
COMMENT ON COLUMN auth_tokens.token_fingerprint IS '原始令牌的 SHA-256 指纹；新镜像优先使用该列认证';
