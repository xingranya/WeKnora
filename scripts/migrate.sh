#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
MIGRATIONS_DIR="${MIGRATIONS_DIR:-${PROJECT_ROOT}/migrations/versioned}"
REQUIRED_MIGRATE_VERSION="4.19.1"
COMMAND="${1:-}"

usage() {
    echo "Usage: $0 {up [steps]|down <steps>|create <migration_name>|version|force <version>|goto <version>}"
}

require_positive_integer() {
    local value="${1:-}"
    local label="$2"
    if [[ ! "${value}" =~ ^[1-9][0-9]*$ ]]; then
        echo "Error: ${label} must be an explicit positive integer" >&2
        exit 1
    fi
}

if [[ -z "${COMMAND}" ]]; then
    usage
    exit 1
fi

if ! command -v migrate >/dev/null 2>&1; then
    echo "Error: migrate tool is not installed" >&2
    echo "Install it with: go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@v${REQUIRED_MIGRATE_VERSION}" >&2
    exit 1
fi

MIGRATE_VERSION_OUTPUT="$(migrate -version 2>&1 || true)"
if [[ "${MIGRATE_VERSION_OUTPUT}" != *"${REQUIRED_MIGRATE_VERSION}"* ]]; then
    echo "Error: migrate ${REQUIRED_MIGRATE_VERSION} is required; installed version is incompatible" >&2
    exit 1
fi

if [[ "${COMMAND}" == "create" ]]; then
    if [[ $# -ne 2 || -z "${2:-}" ]]; then
        echo "Error: migration name is required" >&2
        echo "Usage: $0 create <migration_name>" >&2
        exit 1
    fi
    echo "Creating migration files for $2..."
    migrate create -ext sql -dir "${MIGRATIONS_DIR}" -seq "$2"
    echo "Migration files created in: ${MIGRATIONS_DIR}"
    exit 0
fi

# 先校验破坏性命令的边界，再读取任何数据库配置。
case "${COMMAND}" in
    up)
        if [[ $# -gt 2 ]]; then
            usage
            exit 1
        fi
        if [[ $# -eq 2 ]]; then
            require_positive_integer "$2" "up steps"
        fi
        ;;
    down)
        if [[ $# -ne 2 ]]; then
            echo "Error: down requires an explicit positive step count" >&2
            echo "Usage: $0 down <steps>" >&2
            exit 1
        fi
        require_positive_integer "$2" "down steps"
        ;;
    version)
        if [[ $# -ne 1 ]]; then
            usage
            exit 1
        fi
        ;;
    force)
        if [[ $# -ne 2 || ! "${2:-}" =~ ^(-1|[0-9]+)$ ]]; then
            echo "Error: force version must be -1 or a non-negative integer" >&2
            echo "Usage: $0 force <version>" >&2
            exit 1
        fi
        ;;
    goto)
        if [[ $# -ne 2 || ! "${2:-}" =~ ^[0-9]+$ ]]; then
            echo "Error: goto version must be a non-negative integer" >&2
            echo "Usage: $0 goto <version>" >&2
            exit 1
        fi
        ;;
    *)
        usage
        exit 1
        ;;
esac

build_database_url() {
    if [[ -n "${DB_URL:-}" ]]; then
        # 调用者提供的连接串必须原样传递，尤其不得降低 sslmode。
        printf '%s' "${DB_URL}"
        return
    fi

    local required_name
    for required_name in DB_HOST DB_PORT DB_USER DB_PASSWORD DB_NAME; do
        if [[ -z "${!required_name:-}" ]]; then
            echo "Error: set DB_URL or explicitly provide DB_HOST, DB_PORT, DB_USER, DB_PASSWORD, and DB_NAME" >&2
            return 1
        fi
    done
    if [[ ! "${DB_PORT}" =~ ^[0-9]+$ ]]; then
        echo "Error: DB_PORT must be numeric" >&2
        return 1
    fi

    local ssl_mode="${DB_SSLMODE:-disable}"
    case "${ssl_mode}" in
        disable|allow|prefer|require|verify-ca|verify-full) ;;
        *)
            echo "Error: DB_SSLMODE is invalid" >&2
            return 1
            ;;
    esac
    if ! command -v python3 >/dev/null 2>&1; then
        echo "Error: python3 is required to construct DB_URL safely" >&2
        return 1
    fi

    python3 - "${DB_USER}" "${DB_PASSWORD}" "${DB_HOST}" "${DB_PORT}" "${DB_NAME}" "${ssl_mode}" <<'PY'
import sys
import urllib.parse

user, password, host, port, database, ssl_mode = sys.argv[1:]
if any(char in host for char in "/@?#"):
    raise SystemExit("Error: DB_HOST contains URL delimiter characters")
if ":" in host and not host.startswith("["):
    host = f"[{host}]"
authority = (
    f"{urllib.parse.quote(user, safe='')}:"
    f"{urllib.parse.quote(password, safe='')}@{host}:{port}"
)
path = "/" + urllib.parse.quote(database, safe="")
query = urllib.parse.urlencode({"sslmode": ssl_mode})
print(urllib.parse.urlunsplit(("postgres", authority, path, query, "")), end="")
PY
}

DATABASE_URL="$(build_database_url)"

case "${COMMAND}" in
    up)
        echo "Running migrations up..."
        echo "MIGRATIONS_DIR: ${MIGRATIONS_DIR}"
        if [[ $# -eq 2 ]]; then
            migrate -path "${MIGRATIONS_DIR}" -database "${DATABASE_URL}" up "$2"
        else
            migrate -path "${MIGRATIONS_DIR}" -database "${DATABASE_URL}" up
        fi
        ;;
    down)
        echo "Running $2 down migration(s)..."
        migrate -path "${MIGRATIONS_DIR}" -database "${DATABASE_URL}" down "$2"
        ;;
    version)
        echo "Checking current migration version..."
        migrate -path "${MIGRATIONS_DIR}" -database "${DATABASE_URL}" version
        ;;
    force)
        echo "Forcing migration version to $2..."
        env migrate -path "${MIGRATIONS_DIR}" -database "${DATABASE_URL}" force -- "$2"
        ;;
    goto)
        echo "Migrating to version $2..."
        migrate -path "${MIGRATIONS_DIR}" -database "${DATABASE_URL}" goto "$2"
        ;;
esac

echo "Migration command completed successfully"
