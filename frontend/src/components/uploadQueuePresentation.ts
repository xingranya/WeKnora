import type { UploadQueueStatus } from '@/stores/uploadQueue'

const RETRYABLE_STATUSES = new Set<UploadQueueStatus>(['paused', 'failed'])
const REMOVABLE_STATUSES = new Set<UploadQueueStatus>(['completed', 'failed', 'cancelled'])
const NON_CANCELLABLE_STATUSES = new Set<UploadQueueStatus>([
  'completed',
  'failed',
  'cancel_requested',
  'cancelled',
])

export const uploadTaskCanRetry = (status: UploadQueueStatus) => RETRYABLE_STATUSES.has(status)
export const uploadTaskCanRemove = (status: UploadQueueStatus) => REMOVABLE_STATUSES.has(status)
export const uploadTaskCanCancel = (status: UploadQueueStatus) => !NON_CANCELLABLE_STATUSES.has(status)

// 上传队列任务需要跨路由持续运行，但入口只属于知识库文档上传页。
export const isKnowledgeBaseUploadRoute = (routeName: unknown) =>
  routeName === 'knowledgeBaseDetail' || routeName === 'home'
