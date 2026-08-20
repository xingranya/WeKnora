#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
MIGRATIONS_DIR="${PROJECT_ROOT}/migrations/versioned"
DATABASE_URL="${TEST_DATABASE_URL:-}"

if [[ -z "${DATABASE_URL}" ]]; then
	for required_name in DB_HOST DB_PORT DB_USER DB_PASSWORD DB_NAME; do
		if [[ -z "${!required_name:-}" ]]; then
			echo "Error: set TEST_DATABASE_URL or DB_HOST/DB_PORT/DB_USER/DB_PASSWORD/DB_NAME" >&2
			exit 1
		fi
	done
	DATABASE_URL="$(python3 - "${DB_USER}" "${DB_PASSWORD}" "${DB_HOST}" "${DB_PORT}" "${DB_NAME}" <<'PY'
import sys
import urllib.parse

user, password, host, port, database = sys.argv[1:]
authority = f"{urllib.parse.quote(user, safe='')}:{urllib.parse.quote(password, safe='')}@{host}:{port}"
print(urllib.parse.urlunsplit(("postgres", authority, "/" + urllib.parse.quote(database, safe=""), "sslmode=disable", "")), end="")
PY
)"
fi
for command_name in psql migrate; do
    if ! command -v "${command_name}" >/dev/null 2>&1; then
        echo "Error: ${command_name} is required" >&2
        exit 1
    fi
done

database_name="$(psql "${DATABASE_URL}" -XAtqc 'SELECT current_database()')"
if [[ "${database_name}" != weknora_auth_migration_test_* ]]; then
    echo "Error: refusing destructive test outside a weknora_auth_migration_test_* database" >&2
    exit 1
fi

psql "${DATABASE_URL}" -Xv ON_ERROR_STOP=1 -q <<'SQL'
DROP TABLE IF EXISTS auth_tokens;
DROP TABLE IF EXISTS schema_migrations;

CREATE TABLE auth_tokens (
    id VARCHAR(36) PRIMARY KEY,
    user_id VARCHAR(36) NOT NULL,
    token TEXT NOT NULL,
    token_type VARCHAR(32) NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    is_revoked BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_auth_tokens_token ON auth_tokens(token);

INSERT INTO auth_tokens (id, user_id, token, token_type, expires_at)
VALUES
    ('legacy-access', 'user-1', 'synthetic.legacy.access', 'access_token', NOW() + INTERVAL '1 hour'),
    ('legacy-refresh-a', 'user-1', 'synthetic.legacy.refresh', 'refresh_token', NOW() + INTERVAL '24 hours'),
    ('legacy-refresh-b', 'user-1', 'synthetic.legacy.refresh', 'refresh_token', NOW() + INTERVAL '24 hours');

CREATE TABLE schema_migrations (
    version BIGINT NOT NULL,
    dirty BOOLEAN NOT NULL
);
INSERT INTO schema_migrations(version, dirty) VALUES (86, FALSE);
SQL

migrate -path "${MIGRATIONS_DIR}" -database "${DATABASE_URL}" up 1 >/dev/null

psql "${DATABASE_URL}" -Xv ON_ERROR_STOP=1 -q <<'SQL'
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name = 'auth_tokens'
          AND column_name = 'token_fingerprint'
    ) THEN
        RAISE EXCEPTION 'token_fingerprint column is missing';
    END IF;
    IF (SELECT version <> 87 OR dirty FROM schema_migrations) THEN
        RAISE EXCEPTION 'schema_migrations is not 87 clean';
    END IF;
    IF EXISTS (
        SELECT 1 FROM auth_tokens
        WHERE token NOT LIKE 'synthetic.legacy.%'
           OR token_fingerprint <> encode(sha256(convert_to(token, 'UTF8')), 'hex')
    ) THEN
        RAISE EXCEPTION 'legacy token or fingerprint was changed incorrectly';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_indexes
        WHERE schemaname = 'public'
          AND indexname = 'idx_auth_tokens_token_fingerprint'
    ) THEN
        RAISE EXCEPTION 'fingerprint index is missing';
    END IF;
    IF (SELECT COUNT(*) FROM auth_tokens
        WHERE token = 'synthetic.legacy.refresh' AND NOT is_revoked) <> 1 THEN
        RAISE EXCEPTION 'duplicate active refresh tokens were not collapsed';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_indexes
        WHERE schemaname = 'public'
          AND indexname = 'idx_auth_tokens_token_fingerprint'
          AND indexdef ILIKE 'CREATE UNIQUE INDEX%'
          AND indexdef ILIKE '%is_revoked = false%'
    ) THEN
        RAISE EXCEPTION 'fingerprint index is not unique for active tokens';
    END IF;
END $$;

-- 模拟新 app 双写以及旧 app 在 87 schema 上继续写明文行。
BEGIN;
INSERT INTO auth_tokens (id, user_id, token, token_type, expires_at)
VALUES ('new-access', 'user-2', 'synthetic.new.access', 'access_token', NOW() + INTERVAL '1 hour');
UPDATE auth_tokens
SET token_fingerprint = encode(sha256(convert_to(token, 'UTF8')), 'hex')
WHERE id = 'new-access';
COMMIT;

INSERT INTO auth_tokens (id, user_id, token, token_type, expires_at)
VALUES ('mixed-refresh', 'user-2', 'synthetic.mixed.refresh', 'refresh_token', NOW() + INTERVAL '24 hours');

-- 模拟新 app 指纹优先、明文回退并补写，以及 revoke/logout。
UPDATE auth_tokens
SET token_fingerprint = encode(sha256(convert_to('synthetic.mixed.refresh', 'UTF8')), 'hex')
WHERE token = 'synthetic.mixed.refresh'
  AND token_fingerprint IS NULL;
UPDATE auth_tokens SET is_revoked = TRUE WHERE id = 'legacy-access';
UPDATE auth_tokens SET is_revoked = TRUE WHERE user_id = 'user-2';

DO $$
BEGIN
    IF (SELECT COUNT(*) FROM auth_tokens WHERE token = 'synthetic.legacy.refresh' AND NOT is_revoked) <> 1 THEN
        RAISE EXCEPTION 'old app raw-token lookup failed';
    END IF;
    IF (SELECT COUNT(*) FROM auth_tokens
        WHERE token_fingerprint = encode(sha256(convert_to('synthetic.new.access', 'UTF8')), 'hex')) <> 1 THEN
        RAISE EXCEPTION 'new app fingerprint lookup failed';
    END IF;
    IF EXISTS (SELECT 1 FROM auth_tokens WHERE user_id = 'user-2' AND NOT is_revoked) THEN
        RAISE EXCEPTION 'logout/revoke state was not persisted';
    END IF;
END $$;
SQL

# 直接重放 up SQL，验证 IF NOT EXISTS/IS DISTINCT FROM 可以安全收敛。
psql "${DATABASE_URL}" -Xv ON_ERROR_STOP=1 -q -f \
    "${MIGRATIONS_DIR}/000087_auth_token_fingerprints.up.sql"

migrate -path "${MIGRATIONS_DIR}" -database "${DATABASE_URL}" down 1 >/dev/null

psql "${DATABASE_URL}" -Xv ON_ERROR_STOP=1 -q <<'SQL'
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name = 'auth_tokens'
          AND column_name = 'token_fingerprint'
    ) THEN
        RAISE EXCEPTION 'down did not remove token_fingerprint';
    END IF;
    IF (SELECT version <> 86 OR dirty FROM schema_migrations) THEN
        RAISE EXCEPTION 'schema_migrations is not 86 clean after down';
    END IF;
    IF (SELECT COUNT(*) FROM auth_tokens WHERE token = 'synthetic.legacy.refresh' AND NOT is_revoked) <> 1 THEN
        RAISE EXCEPTION 'down broke old app raw-token lookup';
    END IF;
    IF EXISTS (SELECT 1 FROM auth_tokens WHERE user_id = 'user-2' AND NOT is_revoked) THEN
        RAISE EXCEPTION 'down lost revoke/logout state';
    END IF;
END $$;
SQL

echo "PASS: PostgreSQL auth migration supports expand, mixed app access, revoke/logout, idempotency, and down"
