#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXTENSION_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
REPO_DIR="$(cd "${EXTENSION_DIR}/.." && pwd)"
CHROME_PATH="${CHROME_PATH:-/Applications/Google Chrome.app/Contents/MacOS/Google Chrome}"
KEY_PATH="${JIWAI_EXTENSION_KEY_PATH:-${HOME}/.config/weknora/jiwai-extension.pem}"
OUTPUT_DIR="${REPO_DIR}/frontend/public/downloads"
VERSION="$(jq -r '.version' "${EXTENSION_DIR}/manifest.json")"
CRX_OUTPUT="${OUTPUT_DIR}/jiwai-knowledge-assistant-${VERSION}.crx"
ZIP_OUTPUT="${OUTPUT_DIR}/jiwai-knowledge-assistant-${VERSION}.zip"
STAGING_DIR="$(mktemp -d /tmp/jiwai-extension-build.XXXXXX)"

cleanup() {
  rm -rf "${STAGING_DIR}"
  rm -f "${STAGING_DIR}.crx" "${STAGING_DIR}.pem"
}
trap cleanup EXIT

mkdir -p "${STAGING_DIR}/icons" "${OUTPUT_DIR}"
cp \
  "${EXTENSION_DIR}/manifest.json" \
  "${EXTENSION_DIR}/network.js" \
  "${EXTENSION_DIR}/popup-auth.js" \
  "${EXTENSION_DIR}/background.js" \
  "${EXTENSION_DIR}/collection.js" \
  "${EXTENSION_DIR}/content.js" \
  "${EXTENSION_DIR}/content.css" \
  "${EXTENSION_DIR}/defuddle.js" \
  "${EXTENSION_DIR}/extractors.js" \
  "${EXTENSION_DIR}/popup.js" \
  "${EXTENSION_DIR}/popup.html" \
  "${EXTENSION_DIR}/sidepanel.js" \
  "${EXTENSION_DIR}/sidepanel.html" \
  "${EXTENSION_DIR}/guide.html" \
  "${STAGING_DIR}/"
cp "${EXTENSION_DIR}"/icons/*.png "${STAGING_DIR}/icons/"

if [[ ! -f "${KEY_PATH}" ]]; then
  echo "Error: existing extension signing key is required: ${KEY_PATH}" >&2
  exit 1
fi
EXPECTED_PUBLIC_KEY="$(jq -r '.key' "${EXTENSION_DIR}/manifest.json")"
ACTUAL_PUBLIC_KEY="$(openssl pkey -in "${KEY_PATH}" -pubout -outform DER 2>/dev/null | openssl base64 -A)"
if [[ -z "${ACTUAL_PUBLIC_KEY}" || "${ACTUAL_PUBLIC_KEY}" != "${EXPECTED_PUBLIC_KEY}" ]]; then
  echo "Error: extension signing key does not match manifest public key" >&2
  exit 1
fi

"${CHROME_PATH}" \
  --pack-extension="${STAGING_DIR}" \
  --pack-extension-key="${KEY_PATH}" \
  --no-message-box

cp "${STAGING_DIR}.crx" "${CRX_OUTPUT}"
rm -f "${ZIP_OUTPUT}"
(
  cd "${STAGING_DIR}"
  zip -qr "${ZIP_OUTPUT}" .
)
chmod 644 "${CRX_OUTPUT}" "${ZIP_OUTPUT}"

printf 'CRX: %s\nZIP: %s\n' "${CRX_OUTPUT}" "${ZIP_OUTPUT}"
