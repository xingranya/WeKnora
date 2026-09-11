export const JIWAI_CLI_RELEASE = Object.freeze({
  version: 'company-2ca071fa',
  sourceUrl: 'https://github.com/Tencent/WeKnora.git',
  upstreamCommit: '7a98a8e3de5a9aa85c8f1fa6b02772986cb435d0',
  patchUrl: 'https://know.seeway.co/downloads/jiwai-cli-company-2ca071fa.patch',
  patchSHA256: 'cb518f9bb8f2324272f0ee795d5a226c31bc8303290f8472acd82b0e1813ba3d',
  buildFlags: '-X github.com/Tencent/WeKnora/cli/internal/build.Version=company-2ca071fa -X github.com/Tencent/WeKnora/cli/internal/build.Commit=2ca071fa',
})

/** 固定源码与补丁校验通过后才编译，任一步失败都会停止后续命令。 */
export function buildCLIInstallCommand(): string {
  const release = JIWAI_CLI_RELEASE
  return `JIWAI_CLI_BUILD_DIR="$(mktemp -d)" &&
git clone --filter=blob:none --no-checkout ${release.sourceUrl} "$JIWAI_CLI_BUILD_DIR" &&
git -C "$JIWAI_CLI_BUILD_DIR" checkout --detach ${release.upstreamCommit} &&
curl -fL '${release.patchUrl}' -o "$JIWAI_CLI_BUILD_DIR/jiwai-company.patch" &&
(if command -v sha256sum >/dev/null 2>&1; then
  printf '%s  %s\\n' '${release.patchSHA256}' "$JIWAI_CLI_BUILD_DIR/jiwai-company.patch" | sha256sum -c -
else
  printf '%s  %s\\n' '${release.patchSHA256}' "$JIWAI_CLI_BUILD_DIR/jiwai-company.patch" | shasum -a 256 -c -
fi) &&
git -C "$JIWAI_CLI_BUILD_DIR" apply --check "$JIWAI_CLI_BUILD_DIR/jiwai-company.patch" &&
git -C "$JIWAI_CLI_BUILD_DIR" apply "$JIWAI_CLI_BUILD_DIR/jiwai-company.patch" &&
JIWAI_CLI_BIN_DIR="$HOME/.local/bin" &&
mkdir -p "$JIWAI_CLI_BIN_DIR" &&
JIWAI_CLI_NEW_BINARY="$(mktemp "$JIWAI_CLI_BIN_DIR/weknora.new.XXXXXX")" &&
(cd "$JIWAI_CLI_BUILD_DIR/cli" && go build -ldflags '${release.buildFlags}' -o "$JIWAI_CLI_NEW_BINARY" .) &&
chmod 755 "$JIWAI_CLI_NEW_BINARY" &&
"$JIWAI_CLI_NEW_BINARY" --version &&
"$JIWAI_CLI_NEW_BINARY" mcp serve --help &&
(if [ -e "$JIWAI_CLI_BIN_DIR/weknora" ] || [ -L "$JIWAI_CLI_BIN_DIR/weknora" ]; then
  JIWAI_CLI_BACKUP="$(mktemp "$JIWAI_CLI_BIN_DIR/weknora.backup.XXXXXX")" &&
  cp -pP "$JIWAI_CLI_BIN_DIR/weknora" "$JIWAI_CLI_BACKUP"
fi) &&
mv -f "$JIWAI_CLI_NEW_BINARY" "$JIWAI_CLI_BIN_DIR/weknora" &&
export PATH="$JIWAI_CLI_BIN_DIR:$PATH" &&
"$JIWAI_CLI_BIN_DIR/weknora" --version &&
"$JIWAI_CLI_BIN_DIR/weknora" mcp serve --help`
}

/** CLI 自行追加 /api/v1；保留反向代理前缀，未解析到真实服务时返回空值。 */
export function resolveCLIServiceRoot(apiBaseUrl: string, origin: string): string {
  if (!apiBaseUrl.trim()) return ''
  try {
    const url = new URL(apiBaseUrl, origin && origin !== 'null' ? origin : undefined)
    if ((url.protocol === 'https:' || url.protocol === 'http:') && !url.username && !url.password) {
      const path = url.pathname.replace(/\/+$/, '').replace(/\/api\/v1$/, '')
      return `${url.origin}${path}`
    }
  } catch {
    // Desktop bindings can still be loading when the page first renders.
  }
  return ''
}

export function buildCLIConnectCommand(apiBaseUrl: string, origin: string): string {
  const host = resolveCLIServiceRoot(apiBaseUrl, origin) || 'https://your-server.com'
  // POSIX shell quoting prevents URL characters from becoming shell syntax.
  const quotedHost = `'${host.replace(/'/g, `'"'"'`)}'`
  return `weknora profile add weknora --host ${quotedHost} --use &&\nweknora auth login`
}
