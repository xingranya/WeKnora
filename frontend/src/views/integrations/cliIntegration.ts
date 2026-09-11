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
