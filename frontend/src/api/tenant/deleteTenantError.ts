export interface TenantDeleteFailure {
  blockedByResources: boolean
  code?: number
  status?: number
  message?: string
}

/**
 * 统一解析空间删除失败，避免页面通过服务端文案判断资源冲突。
 */
export function classifyTenantDeleteError(error: any): TenantDeleteFailure {
  const nestedError = error && typeof error.error === 'object' ? error.error : undefined
  const code = typeof nestedError?.code === 'number'
    ? nestedError.code
    : (typeof error?.code === 'number' ? error.code : undefined)
  const status = typeof error?.status === 'number' ? error.status : undefined
  const message = typeof nestedError?.message === 'string'
    ? nestedError.message
    : (typeof error?.message === 'string' ? error.message : undefined)

  return {
    blockedByResources: code === 1005 || status === 409,
    code,
    status,
    message,
  }
}
