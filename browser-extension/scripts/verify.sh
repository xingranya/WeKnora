#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXTENSION_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
CHROME_PATH="${CHROME_PATH:-/Applications/Google Chrome.app/Contents/MacOS/Google Chrome}"
node --check "${EXTENSION_DIR}/background.js"
node --check "${EXTENSION_DIR}/content.js"
node --check "${EXTENSION_DIR}/extractors.js"
node --check "${EXTENSION_DIR}/popup.js"
node --check "${EXTENSION_DIR}/sidepanel.js"
jq -e '.manifest_version == 3 and .version == "1.1.0" and (.content_scripts[0].js | index("extractors.js"))' \
  "${EXTENSION_DIR}/manifest.json" >/dev/null

CHROME_PATH="${CHROME_PATH}" node "${EXTENSION_DIR}/tests/run-browser-test.mjs"
printf 'EXTENSION_VERIFY_OK\n'
