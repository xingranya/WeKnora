#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MIGRATE_SCRIPT="${SCRIPT_DIR}/migrate.sh"
TEST_DIR="$(mktemp -d)"
trap 'rm -rf "${TEST_DIR}"' EXIT

FAKE_BIN="${TEST_DIR}/fake bin"
MIGRATIONS_DIR="${TEST_DIR}/migrations with spaces"
ARGS_FILE="${TEST_DIR}/migrate-args"
INJECTION_MARKER="${TEST_DIR}/python-source-injection"
mkdir -p "${FAKE_BIN}" "${MIGRATIONS_DIR}"

cat > "${FAKE_BIN}/migrate" <<'STUB'
#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == "-version" ]]; then
    printf '%s\n' "${FAKE_MIGRATE_VERSION:-4.19.1}"
    exit 0
fi
: "${MIGRATE_ARGS_FILE:?}"
: > "${MIGRATE_ARGS_FILE}"
for arg in "$@"; do
    printf '%s\n' "${arg}" >> "${MIGRATE_ARGS_FILE}"
done
printf '%s\n' "migration stub completed"
STUB
chmod +x "${FAKE_BIN}/migrate"

assert_not_contains() {
    local content="$1"
    local forbidden="$2"
    if [[ "${content}" == *"${forbidden}"* ]]; then
        printf 'FAIL: output leaked %q\n%s\n' "${forbidden}" "${content}" >&2
        exit 1
    fi
}

assert_arg() {
    local expected="$1"
    if ! grep -Fqx -- "${expected}" "${ARGS_FILE}"; then
        printf 'FAIL: migrate did not receive one exact argument: %q\n' "${expected}" >&2
        exit 1
    fi
}

if grep -Eq '^[[:space:]]*(source|\.)[[:space:]].*\.env' "${MIGRATE_SCRIPT}"; then
    printf 'FAIL: migrate.sh must not execute .env as shell code\n' >&2
    exit 1
fi

DB_USER_VALUE="migration_private_user"
DB_PASSWORD_VALUE="pwn');__import__('pathlib').Path('${INJECTION_MARKER}').touch();#"
DB_HOST_VALUE="private-db.internal"
DB_NAME_VALUE="private_database"
ENCODED_PASSWORD="$(python3 -c 'import sys, urllib.parse; print(urllib.parse.quote(sys.argv[1], safe=""))' "${DB_PASSWORD_VALUE}")"
EXPECTED_DB_URL="postgres://${DB_USER_VALUE}:${ENCODED_PASSWORD}@${DB_HOST_VALUE}:5432/${DB_NAME_VALUE}?sslmode=disable"

OUTPUT="$(
    PATH="${FAKE_BIN}:${PATH}" \
    MIGRATE_ARGS_FILE="${ARGS_FILE}" \
    MIGRATIONS_DIR="${MIGRATIONS_DIR}" \
    DB_URL="" \
    DB_USER="${DB_USER_VALUE}" \
    DB_PASSWORD="${DB_PASSWORD_VALUE}" \
    DB_HOST="${DB_HOST_VALUE}" \
    DB_PORT="5432" \
    DB_NAME="${DB_NAME_VALUE}" \
    "${MIGRATE_SCRIPT}" up 2>&1
)"

for forbidden in \
    "${DB_USER_VALUE}" \
    "${DB_PASSWORD_VALUE}" \
    "${ENCODED_PASSWORD}" \
    "${DB_HOST_VALUE}" \
    "${DB_NAME_VALUE}" \
    "postgres://"
do
    assert_not_contains "${OUTPUT}" "${forbidden}"
done

if [[ -e "${INJECTION_MARKER}" ]]; then
    printf 'FAIL: DB_PASSWORD was executed as Python source\n' >&2
    exit 1
fi

assert_arg "-path"
assert_arg "${MIGRATIONS_DIR}"
assert_arg "-database"
assert_arg "${EXPECTED_DB_URL}"
assert_arg "up"

PROVIDED_DB_URL="postgres://hidden-user:hidden-password@hidden-host/hidden-db?sslmode=require&application_name=migration-test"
TLS_OUTPUT="$(
    PATH="${FAKE_BIN}:${PATH}" \
    MIGRATE_ARGS_FILE="${ARGS_FILE}" \
    MIGRATIONS_DIR="${MIGRATIONS_DIR}" \
    DB_URL="${PROVIDED_DB_URL}" \
    "${MIGRATE_SCRIPT}" up 1 2>&1
)"
assert_not_contains "${TLS_OUTPUT}" "hidden-password"
assert_arg "-database"
assert_arg "${PROVIDED_DB_URL}"
assert_arg "up"
assert_arg "1"
if grep -Fqx -- "${PROVIDED_DB_URL/sslmode=require/sslmode=disable}" "${ARGS_FILE}"; then
    printf 'FAIL: provided DB_URL TLS mode was downgraded\n' >&2
    exit 1
fi

if DOWN_OUTPUT="$(
    PATH="${FAKE_BIN}:${PATH}" \
    MIGRATE_ARGS_FILE="${ARGS_FILE}" \
    DB_URL="${PROVIDED_DB_URL}" \
    "${MIGRATE_SCRIPT}" down 2>&1
)"; then
    printf 'FAIL: down without an explicit step count succeeded\n' >&2
    exit 1
fi
assert_not_contains "${DOWN_OUTPUT}" "hidden-password"

if PATH="${FAKE_BIN}:${PATH}" \
    MIGRATE_ARGS_FILE="${ARGS_FILE}" \
    DB_URL="${PROVIDED_DB_URL}" \
    "${MIGRATE_SCRIPT}" down 0 >/dev/null 2>&1; then
    printf 'FAIL: down accepted a non-positive step count\n' >&2
    exit 1
fi

DOWN_OUTPUT="$(
    PATH="${FAKE_BIN}:${PATH}" \
    MIGRATE_ARGS_FILE="${ARGS_FILE}" \
    MIGRATIONS_DIR="${MIGRATIONS_DIR}" \
    DB_URL="${PROVIDED_DB_URL}" \
    "${MIGRATE_SCRIPT}" down 1 2>&1
)"
assert_not_contains "${DOWN_OUTPUT}" "hidden-password"
assert_arg "down"
assert_arg "1"

CREATE_NAME="release candidate"
CREATE_OUTPUT="$(
    PATH="${FAKE_BIN}:${PATH}" \
    MIGRATE_ARGS_FILE="${ARGS_FILE}" \
    MIGRATIONS_DIR="${MIGRATIONS_DIR}" \
    DB_URL="postgres://hidden-user:hidden-password@hidden-host/hidden-db?sslmode=require" \
    "${MIGRATE_SCRIPT}" create "${CREATE_NAME}" 2>&1
)"
assert_not_contains "${CREATE_OUTPUT}" "postgres://"
assert_not_contains "${CREATE_OUTPUT}" "hidden-password"
assert_arg "${MIGRATIONS_DIR}"
assert_arg "${CREATE_NAME}"

if PATH="${FAKE_BIN}:${PATH}" \
    FAKE_MIGRATE_VERSION="4.20.0" \
    MIGRATE_ARGS_FILE="${ARGS_FILE}" \
    DB_URL="${PROVIDED_DB_URL}" \
    "${MIGRATE_SCRIPT}" version >/dev/null 2>&1; then
    printf 'FAIL: migrate.sh accepted an unpinned migrate version\n' >&2
    exit 1
fi

printf 'PASS: migrate.sh preserves TLS and argument boundaries, requires bounded down, and pins migrate 4.19.1\n'
