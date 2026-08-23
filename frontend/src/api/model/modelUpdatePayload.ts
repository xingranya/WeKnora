type ModelUpdatePayload = {
  name?: unknown
  type?: unknown
  source?: unknown
  parameters?: Record<string, unknown>
  [key: string]: unknown
}

const hasOwn = (value: object, key: string) => Object.prototype.hasOwnProperty.call(value, key)

/**
 * 主编辑表单发送完整模型字段，但会省略空请求头和 0 并发上限。
 * 在进入 PATCH 接口前把这两个有明确“清空”语义的字段补齐；真正的部分更新保持原样。
 */
export function prepareModelUpdatePayload<T extends ModelUpdatePayload>(data: T): T {
  const parameters = data.parameters
  const isCompleteEditorPayload = hasOwn(data, 'name')
    && hasOwn(data, 'type')
    && hasOwn(data, 'source')
    && parameters !== null
    && typeof parameters === 'object'

  if (!isCompleteEditorPayload || !parameters) {
    return data
  }

  return {
    ...data,
    parameters: {
      ...parameters,
      ...(!hasOwn(parameters, 'custom_headers') ? { custom_headers: {} } : {}),
      ...(!hasOwn(parameters, 'max_concurrency') ? { max_concurrency: 0 } : {}),
    },
  } as T
}
