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
