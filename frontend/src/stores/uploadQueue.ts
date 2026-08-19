import { defineStore } from 'pinia'
import i18n from '@/i18n'
import {
  cancelKnowledgeUpload,
  cancelKnowledgeParse,
  completeKnowledgeUpload,
  getKnowledgeDetails,
  getKnowledgeUpload,
  initializeKnowledgeUpload,
  reparseKnowledge,
  uploadKnowledgePart,
  type KnowledgeUploadSession,
} from '@/api/knowledge-base'
import type { KnowledgeProcessOverrides } from '@/types/knowledgeProcess'
import { generateUUID } from '@/utils/randomUUID'
import { sha256BlobHex } from '@/utils/sha256'

export type UploadQueueStatus =
  | 'queued' | 'uploading' | 'paused' | 'needs_file' | 'completing'
  | 'waiting_parse' | 'parsing' | 'status_unknown' | 'completed' | 'failed'
  | 'cancel_requested' | 'cancelled'

export interface UploadQueueTask {
  id: string
  batchId: string
  kbId: string
  kbName: string
  fileName: string
  fileSize: number
  mimeType: string
  lastModified: number
  targetFolder: string
  status: UploadQueueStatus
  confirmedBytes: number
  displayBytes: number
  speedBps: number
  etaSeconds: number | null
  uploadId?: string
  chunkSize?: number
  knowledgeId?: string
  error?: string
  file?: File
  tagIds?: string[]
  processConfig?: KnowledgeProcessOverrides
  createdAt: number
}

const STORAGE_KEY = 'weknora.uploadQueue.v1'

const queueOwnerKey = () => {
  try {
    const user = JSON.parse(localStorage.getItem('weknora_user') || 'null') as { id?: string } | null
    const tenant = localStorage.getItem('weknora_selected_tenant_id') ||
      (JSON.parse(localStorage.getItem('weknora_tenant') || 'null') as { id?: string } | null)?.id
    if (!user?.id || !tenant) return ''
    return `${user.id}:${tenant}`
  } catch {
    return ''
  }
}

const queueStorageKey = () => {
  const owner = queueOwnerKey()
  return owner ? `${STORAGE_KEY}:${owner}` : ''
}

const persistable = (task: UploadQueueTask) => {
  const { file: _file, ...metadata } = task
  return metadata
}

const verifyConfirmedFileParts = async (
  task: UploadQueueTask,
  session: KnowledgeUploadSession,
  signal: AbortSignal,
  hashBlob: (blob: Blob) => Promise<string>,
) => {
  if (!task.file || session.received_bytes <= 0) return
  const hashes = Object.entries(session.received_part_hashes || {})
    .map(([number, hash]) => [Number(number), hash] as const)
    .filter(([number]) => Number.isInteger(number) && number >= 0)
    .sort((a, b) => a[0] - b[0])
  if (!hashes.length) throw new Error(t('errors.fileMismatch'))
  for (const [partNumber, expectedHash] of hashes) {
    if (signal.aborted) throw new DOMException('Aborted', 'AbortError')
    const start = partNumber * session.chunk_size
    const end = Math.min(start + session.chunk_size, task.fileSize)
    const blob = task.file.slice(start, end)
    if (await hashBlob(blob) !== expectedHash) throw new Error(t('errors.fileMismatch'))
  }
}

const t = (key: string) => i18n.global.t(`knowledgeBase.uploadQueue.${key}`) as string

export interface UploadQueueDependencies {
  cancelKnowledgeUpload: typeof cancelKnowledgeUpload
  cancelKnowledgeParse: typeof cancelKnowledgeParse
  completeKnowledgeUpload: typeof completeKnowledgeUpload
  getKnowledgeDetails: typeof getKnowledgeDetails
  getKnowledgeUpload: typeof getKnowledgeUpload
  initializeKnowledgeUpload: typeof initializeKnowledgeUpload
  reparseKnowledge: typeof reparseKnowledge
  uploadKnowledgePart: typeof uploadKnowledgePart
  hashBlob: (blob: Blob) => Promise<string>
  sleep: (ms: number) => Promise<void>
  now: () => number
  parseStatusUnknownAfterMs: number
  normalPollIntervalMs: number
  unknownPollIntervalMs: number
  dispatchKnowledgeFileUploaded: (kbId: string) => void
}

const defaultDependencies: UploadQueueDependencies = {
  cancelKnowledgeUpload,
  cancelKnowledgeParse,
  completeKnowledgeUpload,
  getKnowledgeDetails,
  getKnowledgeUpload,
  initializeKnowledgeUpload,
  reparseKnowledge,
  uploadKnowledgePart,
  hashBlob: sha256BlobHex,
  sleep: ms => new Promise(resolve => setTimeout(resolve, ms)),
  now: () => performance.now(),
  parseStatusUnknownAfterMs: 30 * 60 * 1000,
  normalPollIntervalMs: 2000,
  unknownPollIntervalMs: 10000,
  dispatchKnowledgeFileUploaded: kbId => {
    window.dispatchEvent(new CustomEvent('knowledgeFileUploaded', { detail: { kbId } }))
  },
}

export const createUploadQueueStore = (
  storeId: string,
  dependencies: UploadQueueDependencies = defaultDependencies,
) => defineStore(storeId, {
  state: () => ({
    tasks: [] as UploadQueueTask[],
    visible: false,
    hydrated: false,
    activeTaskId: '' as string,
    controllers: new Map<string, AbortController>(),
    pollingTaskIds: new Set<string>(),
    cancellationRecoveryTaskIds: new Set<string>(),
    hydratedOwner: '' as string,
  }),
  getters: {
    activeCount: state => state.tasks.filter(task => ['queued', 'uploading', 'completing', 'waiting_parse', 'parsing', 'status_unknown', 'cancel_requested'].includes(task.status)).length,
    unfinishedCount: state => state.tasks.filter(task => !['completed', 'cancelled'].includes(task.status)).length,
  },
  actions: {
    hydrate() {
      const owner = queueOwnerKey()
      if (!owner) return
      if (this.hydrated && this.hydratedOwner === owner) return
      if (this.hydratedOwner && this.hydratedOwner !== owner) {
        this.controllers.forEach(controller => controller.abort())
        this.controllers.clear()
        this.pollingTaskIds.clear()
        this.cancellationRecoveryTaskIds.clear()
        this.activeTaskId = ''
      }
      this.hydrated = true
      this.hydratedOwner = owner
      try {
        const rows = JSON.parse(localStorage.getItem(queueStorageKey()) || '[]') as UploadQueueTask[]
        this.tasks = rows.map(task => ({
          ...task,
          status: ['completed', 'cancelled'].includes(task.status)
            ? task.status
            : task.status === 'cancel_requested' ? 'cancel_requested'
            : task.knowledgeId
              ? (task.status === 'failed' || task.status === 'status_unknown' ? task.status : 'waiting_parse')
              : 'needs_file',
          speedBps: 0,
          etaSeconds: null,
          displayBytes: task.confirmedBytes || 0,
        }))
        for (const task of this.tasks) {
          if (task.knowledgeId && ['waiting_parse', 'parsing', 'status_unknown'].includes(task.status)) {
            void this.pollParsing(task.id, task.knowledgeId)
          }
        }
        void this.recoverPersistedSessions()
      } catch {
        this.tasks = []
      }
    },
    persist() {
      const key = this.hydratedOwner ? `${STORAGE_KEY}:${this.hydratedOwner}` : queueStorageKey()
      if (key) localStorage.setItem(key, JSON.stringify(this.tasks.map(persistable)))
    },
    enqueueFiles(options: {
      kbId: string
      kbName: string
      files: File[]
      targetFolder: string
      fileNames: Array<string | undefined>
      tagIds?: string[]
      processConfig?: KnowledgeProcessOverrides
    }) {
      this.hydrate()
      const batchId = generateUUID()
      options.files.forEach((file, index) => {
        const qualifiedName = options.fileNames[index]
        const segments = qualifiedName?.split('/').filter(Boolean) || []
        const folderPath = segments.length > 1 ? segments.slice(0, -1).join('/') : options.targetFolder
        this.tasks.push({
          id: generateUUID(), batchId, kbId: options.kbId, kbName: options.kbName,
          fileName: file.name, fileSize: file.size, mimeType: file.type || 'application/octet-stream',
          lastModified: file.lastModified, targetFolder: folderPath || '', status: 'queued',
          confirmedBytes: 0, displayBytes: 0, speedBps: 0, etaSeconds: null,
          file, tagIds: options.tagIds, processConfig: options.processConfig, createdAt: Date.now(),
        })
      })
      this.visible = true
      this.persist()
      void this.pump()
    },
    patch(id: string, patch: Partial<UploadQueueTask>) {
      const index = this.tasks.findIndex(task => task.id === id)
      if (index < 0) return
      this.tasks[index] = { ...this.tasks[index], ...patch }
      this.persist()
    },
    async pump() {
      if (this.activeTaskId) return
      const next = this.tasks.find(task => task.status === 'queued' && task.file)
      if (!next) return
      this.activeTaskId = next.id
      try {
        await this.runTask(next.id)
      } finally {
        this.activeTaskId = ''
        void this.pump()
      }
    },
    async ensureSession(task: UploadQueueTask): Promise<KnowledgeUploadSession> {
      if (task.uploadId) {
        const response: any = await dependencies.getKnowledgeUpload(task.kbId, task.uploadId)
        return response.data as KnowledgeUploadSession
      }
      const response: any = await dependencies.initializeKnowledgeUpload(task.kbId, {
        file_name: task.fileName,
        file_size: task.fileSize,
        mime_type: task.mimeType,
        last_modified: task.lastModified,
        folder_path: task.targetFolder,
        tag_ids: task.tagIds,
        channel: 'web',
        process_config: task.processConfig,
      })
      const session = response.data as KnowledgeUploadSession
      this.patch(task.id, { uploadId: session.id, chunkSize: session.chunk_size, confirmedBytes: session.received_bytes, displayBytes: session.received_bytes })
      return session
    },
    async recoverPersistedSessions() {
      for (const task of this.tasks) {
        if (!task.uploadId || task.file || task.knowledgeId || ['completed', 'cancelled'].includes(task.status)) continue
        if (task.status === 'cancel_requested') {
          await this.cancel(task.id)
          continue
        }
        try {
          const response: any = await dependencies.getKnowledgeUpload(task.kbId, task.uploadId)
          const session = response.data as KnowledgeUploadSession
          if (session.status === 'completed' && session.knowledge_id) {
            await this.trackKnowledge(task.id, task, session.knowledge_id)
          } else if (session.status === 'completed_cleanup_pending' && session.knowledge_id) {
            await this.completeAndTrack(task.id, task, session)
          } else if (session.status === 'completing' ||
            (session.status === 'failed' && session.received_bytes === session.file_size)) {
            await this.completeAndTrack(task.id, task, session)
          } else if (session.status === 'cancelled_cleanup_pending') {
            await dependencies.cancelKnowledgeUpload(task.kbId, task.uploadId)
            this.patch(task.id, { status: 'cancelled' })
          } else if (session.status === 'cancelled') {
            this.patch(task.id, { status: 'cancelled' })
          } else if (session.status === 'expired' || session.status === 'expired_cleanup_pending') {
            this.patch(task.id, { status: 'failed', error: t('errors.sessionExpired') })
          }
        } catch (error: any) {
          this.patch(task.id, { status: 'failed', error: error?.message || t('errors.uploadFailed') })
        }
      }
    },
    async trackKnowledge(id: string, task: UploadQueueTask, knowledgeId: string) {
      const current = this.tasks.find(item => item.id === id)
      if (!current) return
      if (current.status === 'cancel_requested') {
        await dependencies.cancelKnowledgeParse(knowledgeId)
        this.patch(id, { status: 'cancelled', knowledgeId, speedBps: 0, etaSeconds: null })
        return
      }
      this.patch(id, { status: 'waiting_parse', knowledgeId, speedBps: 0, etaSeconds: null })
      dependencies.dispatchKnowledgeFileUploaded(task.kbId)
      void this.pollParsing(id, knowledgeId)
    },
    async completeAndTrack(id: string, task: UploadQueueTask, session: KnowledgeUploadSession) {
      const current = this.tasks.find(item => item.id === id)
      if (current?.status !== 'cancel_requested') {
        this.patch(id, { status: 'completing', displayBytes: task.fileSize, confirmedBytes: task.fileSize })
      }
      const completed: any = await dependencies.completeKnowledgeUpload(task.kbId, session.id)
      const knowledgeId = completed?.data?.id || session.knowledge_id
      if (knowledgeId) await this.trackKnowledge(id, task, knowledgeId)
      else this.patch(id, { status: 'completed' })
    },
    async runTask(id: string) {
      const task = this.tasks.find(item => item.id === id)
      if (!task?.file) return
      const controller = new AbortController()
      this.controllers.set(id, controller)
      const startedAt = dependencies.now()
      const startingBytes = task.confirmedBytes
      try {
        let session = await this.ensureSession(task)
        const afterSession = this.tasks.find(item => item.id === id)
        if (!afterSession || afterSession.status === 'cancelled') {
          try { await dependencies.cancelKnowledgeUpload(task.kbId, session.id) } catch { /* 服务端会话可能已被取消 */ }
          return
        }
        if (afterSession.status === 'paused') return
        if (session.status === 'completed' && session.knowledge_id) {
          await this.trackKnowledge(id, task, session.knowledge_id)
          return
        }
        if (session.status === 'completed_cleanup_pending' && session.knowledge_id) {
          await this.completeAndTrack(id, task, session)
          return
        }
        if (session.status === 'cancelled' || session.status === 'cancelled_cleanup_pending') {
          this.patch(id, { status: 'cancelled', error: undefined })
          return
        }
        if (session.status === 'expired' || session.status === 'expired_cleanup_pending') {
          this.patch(id, { status: 'failed', error: t('errors.sessionExpired') })
          return
        }
        if (session.status === 'completing') {
          await this.completeAndTrack(id, task, session)
          return
        }
        await verifyConfirmedFileParts(task, session, controller.signal, dependencies.hashBlob)
        let offset = session.received_bytes
        const chunkSize = session.chunk_size
        this.patch(id, { status: 'uploading', confirmedBytes: offset, displayBytes: offset, error: undefined })
        while (offset < task.fileSize) {
          if (controller.signal.aborted) throw new DOMException('Aborted', 'AbortError')
          const current = this.tasks.find(item => item.id === id)
          if (!current || current.status === 'paused' || ['cancel_requested', 'cancelled'].includes(current.status)) return
          const partNumber = Math.floor(offset / chunkSize)
          const blob = task.file.slice(offset, Math.min(offset + chunkSize, task.fileSize))
          const hash = await dependencies.hashBlob(blob)
          await dependencies.uploadKnowledgePart(task.kbId, session.id, partNumber, blob, offset, task.fileSize, hash, controller.signal, loaded => {
            const elapsed = Math.max((dependencies.now() - startedAt) / 1000, 0.1)
            const sent = offset + Math.min(loaded, blob.size)
            const speed = Math.max(0, (sent - startingBytes) / elapsed)
            this.patch(id, {
              displayBytes: sent,
              speedBps: speed,
              etaSeconds: speed > 0 ? Math.ceil((task.fileSize - sent) / speed) : null,
            })
          })
          const afterPart = this.tasks.find(item => item.id === id)
          if (!afterPart || ['cancel_requested', 'cancelled'].includes(afterPart.status)) return
          offset += blob.size
          this.patch(id, { confirmedBytes: offset, displayBytes: offset })
        }
        await this.completeAndTrack(id, task, session)
      } catch (error: any) {
        if (error?.name === 'AbortError' || error?.code === 'ERR_CANCELED') {
          const current = this.tasks.find(item => item.id === id)
          if (current?.status === 'queued' || current?.status === 'cancel_requested') return
          if (current?.status !== 'cancelled') this.patch(id, { status: 'paused', speedBps: 0, etaSeconds: null })
        } else {
          const current = this.tasks.find(item => item.id === id)
          if (current?.status === 'cancel_requested' || current?.status === 'cancelled') return
          this.patch(id, { status: 'failed', error: error?.message || t('errors.uploadFailed'), speedBps: 0, etaSeconds: null })
        }
      } finally {
        this.controllers.delete(id)
      }
    },
    async pollParsing(taskId: string, knowledgeId: string) {
      if (this.pollingTaskIds.has(taskId)) return
      this.pollingTaskIds.add(taskId)
      const startedAt = dependencies.now()
      let longRunning = this.tasks.find(item => item.id === taskId)?.status === 'status_unknown'
      try {
        while (true) {
          const task = this.tasks.find(item => item.id === taskId)
          if (!task || ['cancel_requested', 'cancelled'].includes(task.status)) return

          let response: any
          try {
            response = await dependencies.getKnowledgeDetails(knowledgeId)
          } catch {
            const current = this.tasks.find(item => item.id === taskId)
            if (!current || ['cancel_requested', 'cancelled', 'completed'].includes(current.status)) return
            this.patch(taskId, { status: 'status_unknown', error: t('errors.statusCheckFailed') })
            await dependencies.sleep(dependencies.unknownPollIntervalMs)
            continue
          }

          const afterResponse = this.tasks.find(item => item.id === taskId)
          if (!afterResponse || ['cancel_requested', 'cancelled'].includes(afterResponse.status)) return
          const status = response?.data?.parse_status
          if (status === 'completed') {
            this.patch(taskId, { status: 'completed', error: undefined })
            return
          }
          if (status === 'failed' || status === 'cancelled') {
            this.patch(taskId, { status: 'failed', error: response?.data?.error_message || status })
            return
          }

          longRunning ||= dependencies.now() - startedAt >= dependencies.parseStatusUnknownAfterMs
          this.patch(taskId, longRunning
            ? { status: 'status_unknown', error: t('errors.parseTakingLonger') }
            : {
                status: status === 'processing' || status === 'finalizing' ? 'parsing' : 'waiting_parse',
                error: undefined,
              })
          await dependencies.sleep(longRunning
            ? dependencies.unknownPollIntervalMs
            : dependencies.normalPollIntervalMs)
        }
      } finally {
        this.pollingTaskIds.delete(taskId)
      }
    },
    pause(id: string) {
      this.patch(id, { status: 'paused', speedBps: 0, etaSeconds: null })
      this.controllers.get(id)?.abort()
    },
    resume(id: string, file?: File) {
      const task = this.tasks.find(item => item.id === id)
      if (!task) return
      if (file && (file.name !== task.fileName || file.size !== task.fileSize || file.lastModified !== task.lastModified)) {
        throw new Error(t('errors.fileMismatch'))
      }
      if (task.knowledgeId && !file && task.status === 'failed') {
        void this.retryParsing(id, task.knowledgeId)
        return
      }
      this.patch(id, { file: file || task.file, status: 'queued', error: undefined })
      void this.pump()
    },
    async retryParsing(id: string, knowledgeId: string) {
      this.patch(id, { status: 'waiting_parse', error: undefined })
      let response: any
      try {
        response = await dependencies.getKnowledgeDetails(knowledgeId)
      } catch {
        const current = this.tasks.find(item => item.id === id)
        if (!current || ['completed', 'cancelled'].includes(current.status)) return
        this.patch(id, { status: 'status_unknown', error: t('errors.statusCheckFailed') })
        void this.pollParsing(id, knowledgeId)
        return
      }

      const status = response?.data?.parse_status
      const currentAfterLookup = this.tasks.find(item => item.id === id)
      if (!currentAfterLookup || ['cancel_requested', 'cancelled', 'completed'].includes(currentAfterLookup.status)) return
      if (status === 'completed') {
        this.patch(id, { status: 'completed', error: undefined })
        return
      }
      if (status !== 'failed' && status !== 'cancelled') {
        this.patch(id, {
          status: status === 'processing' || status === 'finalizing' ? 'parsing' : 'waiting_parse',
          error: undefined,
        })
        await this.pollParsing(id, knowledgeId)
        return
      }

      try {
        const currentBeforeRetry = this.tasks.find(item => item.id === id)
        if (!currentBeforeRetry || ['cancel_requested', 'cancelled', 'completed'].includes(currentBeforeRetry.status)) return
        await dependencies.reparseKnowledge(knowledgeId)
        await this.pollParsing(id, knowledgeId)
      } catch (error: any) {
        const current = this.tasks.find(item => item.id === id)
        if (!current || ['completed', 'cancelled'].includes(current.status)) return
        this.patch(id, { status: 'failed', error: error?.message || t('errors.parseRetryFailed') })
      }
    },
    async reconcileCancellation(id: string) {
      if (this.cancellationRecoveryTaskIds.has(id)) return
      this.cancellationRecoveryTaskIds.add(id)
      try {
        while (true) {
          const task = this.tasks.find(item => item.id === id)
          if (!task || task.status !== 'cancel_requested') return
          try {
            if (task.knowledgeId) {
              try {
                await dependencies.cancelKnowledgeParse(task.knowledgeId)
                this.patch(id, { status: 'cancelled', speedBps: 0, etaSeconds: null, error: undefined })
                return
              } catch {
                const response: any = await dependencies.getKnowledgeDetails(task.knowledgeId)
                const parseStatus = response?.data?.parse_status
                if (parseStatus === 'completed') {
                  this.patch(id, { status: 'completed', error: undefined })
                  return
                }
                if (parseStatus === 'failed') {
                  this.patch(id, {
                    status: 'failed',
                    error: response?.data?.error_message || parseStatus,
                  })
                  return
                }
                if (parseStatus === 'cancelled') {
                  this.patch(id, { status: 'cancelled', error: undefined })
                  return
                }
              }
            } else if (task.uploadId) {
              try {
                await dependencies.cancelKnowledgeUpload(task.kbId, task.uploadId)
                this.patch(id, { status: 'cancelled', speedBps: 0, etaSeconds: null, error: undefined })
                return
              } catch {
                const response: any = await dependencies.getKnowledgeUpload(task.kbId, task.uploadId)
                const session = response.data as KnowledgeUploadSession
                if (session.status === 'cancelled' || session.status === 'cancelled_cleanup_pending' ||
                  session.status === 'expired' || session.status === 'expired_cleanup_pending') {
                  this.patch(id, { status: 'cancelled', error: undefined })
                  return
                }
                if (session.knowledge_id) {
                  this.patch(id, { knowledgeId: session.knowledge_id })
                  continue
                }
                if (session.status === 'completed' || session.status === 'completed_cleanup_pending') {
                  this.patch(id, { status: 'completed', error: undefined })
                  return
                }
                if (session.status === 'completing' ||
                  (session.status === 'failed' && session.received_bytes === session.file_size)) {
                  const completed: any = await dependencies.completeKnowledgeUpload(task.kbId, session.id)
                  const knowledgeId = completed?.data?.id
                  if (knowledgeId) {
                    this.patch(id, { knowledgeId })
                    continue
                  }
                }
              }
            } else {
              this.patch(id, { status: 'cancelled', error: undefined })
              return
            }
          } catch {
            // 网络错误不改变取消意图，等待下一次服务端确认。
          }

          const latest = this.tasks.find(item => item.id === id)
          if (!latest || latest.status !== 'cancel_requested') return
          this.patch(id, { error: t('errors.cancelStatusUnknown') })
          await dependencies.sleep(dependencies.unknownPollIntervalMs)
        }
      } finally {
        this.cancellationRecoveryTaskIds.delete(id)
      }
    },
    async cancel(id: string) {
      const task = this.tasks.find(item => item.id === id)
      if (!task) return
      this.patch(id, { status: 'cancel_requested', speedBps: 0, etaSeconds: null, error: undefined })
      this.controllers.get(id)?.abort()
      if (!task.uploadId && !task.knowledgeId) {
        this.patch(id, { status: 'cancelled', speedBps: 0, etaSeconds: null })
        return
      }
      try {
        if (task.knowledgeId) {
          await dependencies.cancelKnowledgeParse(task.knowledgeId)
        } else if (task.uploadId) {
          await dependencies.cancelKnowledgeUpload(task.kbId, task.uploadId)
        }
        this.patch(id, { status: 'cancelled', speedBps: 0, etaSeconds: null })
      } catch (error: any) {
        const latest = this.tasks.find(item => item.id === id)
        if (!latest || latest.status !== 'cancel_requested') return
        this.patch(id, { error: t('errors.cancelStatusUnknown'), speedBps: 0, etaSeconds: null })
        void this.reconcileCancellation(id)
      }
    },
    remove(id: string) {
      this.tasks = this.tasks.filter(task => task.id !== id)
      this.persist()
    },
  },
})

export const useUploadQueueStore = createUploadQueueStore('uploadQueue')
