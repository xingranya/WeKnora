-- 平台解析引擎配置为全局单例，不归属于任何空间。
-- 配置包含连接凭据，因此使用独立表，避免被普通系统设置列表返回。
CREATE TABLE IF NOT EXISTS platform_parser_engine_configs (
    id BIGINT PRIMARY KEY,
    config JSONB NOT NULL DEFAULT '{}'::jsonb,
    last_modified_by VARCHAR(36) NOT NULL DEFAULT '',
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 升级前解析配置属于空间。平台配置只能保存一份，因此只有历史配置唯一时才能
-- 自动迁移；若多个空间配置不同，必须先人工确认平台应采用哪一份，避免静默覆盖。
DO $$
BEGIN
    IF (
        SELECT COUNT(DISTINCT parser_engine_config::text)
        FROM tenants
        WHERE parser_engine_config IS NOT NULL
          AND jsonb_typeof(parser_engine_config) = 'object'
          AND parser_engine_config <> '{}'::jsonb
    ) > 1 THEN
        RAISE EXCEPTION '检测到多份不一致的空间解析引擎配置，请先合并后再升级';
    END IF;
END $$;

INSERT INTO platform_parser_engine_configs (id, config, last_modified_by)
SELECT 1, parser_engine_config, ''
FROM tenants
WHERE parser_engine_config IS NOT NULL
  AND jsonb_typeof(parser_engine_config) = 'object'
  AND parser_engine_config <> '{}'::jsonb
ORDER BY id
LIMIT 1
ON CONFLICT (id) DO NOTHING;
