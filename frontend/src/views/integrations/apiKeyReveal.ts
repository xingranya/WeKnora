/**
 * 从即时读取接口中提取完整 API Key，并拒绝列表接口使用的脱敏形式。
 * 该校验让复制和安装提示词生成在后端误返回掩码时失败关闭，避免把无效凭据交给用户或 AI。
 */
export function extractRevealedAPIKeyToken(response: {
  success: boolean
  data?: { token?: unknown }
}): string {
  if (!response.success || typeof response.data?.token !== 'string') return ''
  const token = response.data.token.trim()
  if (!token || token.includes('*') || /^.{7}\.\.\..{4}$/.test(token)) return ''
  return token
}
