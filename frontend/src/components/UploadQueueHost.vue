<template>
  <div v-if="authStore.isLoggedIn && isKnowledgeBaseUploadRoute(route.name) && (tasks.length || queue.visible)" class="upload-queue-host">
    <t-tooltip :content="t('knowledgeBase.uploadQueue.title')" placement="left">
      <button type="button" class="upload-queue-trigger"
        :aria-label="t('knowledgeBase.uploadQueue.title')" aria-controls="upload-queue-panel"
        :aria-expanded="queue.visible" @click="queue.visible = !queue.visible">
        <t-icon name="upload" size="20px" aria-hidden="true" />
        <span v-if="activeCount" class="upload-queue-badge" aria-hidden="true">{{ activeCount }}</span>
      </button>
    </t-tooltip>

    <section v-if="queue.visible" id="upload-queue-panel" class="upload-queue-panel" role="region"
      :aria-label="t('knowledgeBase.uploadQueue.title')">
      <header class="upload-queue-header">
        <div>
          <strong>{{ t('knowledgeBase.uploadQueue.title') }}</strong>
          <span>{{ t('knowledgeBase.uploadQueue.summary', { count: unfinishedCount }) }}</span>
        </div>
        <button type="button" class="queue-icon-btn" :title="t('general.close')"
          :aria-label="t('general.close')" @click="queue.visible = false">
          <t-icon name="close" aria-hidden="true" />
        </button>
      </header>

      <div class="upload-queue-list">
        <article v-for="task in orderedTasks" :key="task.id" class="upload-queue-item"
          :aria-labelledby="`upload-task-${task.id}`">
          <div class="upload-item-main">
            <t-icon name="file" class="upload-file-icon" aria-hidden="true" />
            <div class="upload-file-copy">
              <div :id="`upload-task-${task.id}`" class="upload-file-title" :title="task.fileName">{{ task.fileName }}</div>
              <div class="upload-file-meta">
                <span>{{ task.kbName }}</span>
                <span role="status" aria-live="polite">{{ statusLabel(task.status) }}</span>
              </div>
            </div>
            <div class="upload-file-percent" aria-hidden="true">{{ progress(task) }}%</div>
          </div>

          <div class="upload-progress-track" role="progressbar" aria-valuemin="0" aria-valuemax="100"
            :aria-valuenow="progress(task)"
            :aria-label="t('knowledgeBase.uploadQueue.progress', { file: task.fileName, percent: progress(task) })">
            <div :class="['upload-progress-value', { failed: task.status === 'failed' }]" :style="{ width: `${progress(task)}%` }" />
          </div>

          <div class="upload-item-footer">
            <div class="upload-file-detail">
              <span>{{ formatBytes(task.displayBytes) }} / {{ formatBytes(task.fileSize) }}</span>
              <span v-if="task.speedBps > 0">{{ formatBytes(task.speedBps) }}/s</span>
              <span v-if="task.etaSeconds !== null">{{ formatDuration(task.etaSeconds) }}</span>
              <span v-if="task.error"
                :class="task.status === 'status_unknown' ? 'upload-file-notice' : 'upload-file-error'"
                :title="task.error">{{ task.error }}</span>
            </div>
            <div class="upload-item-actions">
              <t-tooltip v-if="task.status === 'uploading'" :content="t('knowledgeBase.uploadQueue.pause')">
                <button type="button" class="queue-icon-btn" :aria-label="t('knowledgeBase.uploadQueue.pause')"
                  @click="queue.pause(task.id)"><t-icon name="pause" aria-hidden="true" /></button>
              </t-tooltip>
              <t-tooltip v-if="uploadTaskCanRetry(task.status)" :content="t('knowledgeBase.uploadQueue.retry')">
                <button type="button" class="queue-icon-btn" :aria-label="t('knowledgeBase.uploadQueue.retry')"
                  @click="resume(task.id)"><t-icon name="play" aria-hidden="true" /></button>
              </t-tooltip>
              <template v-if="task.status === 'needs_file'">
                <t-tooltip :content="t('knowledgeBase.uploadQueue.selectFile')">
                  <button type="button" class="queue-file-picker"
                    :aria-label="t('knowledgeBase.uploadQueue.selectFile')" @click="openFilePicker(task.id)">
                    <t-icon name="folder-open" aria-hidden="true" />
                  </button>
                </t-tooltip>
                <input :ref="element => setFileInput(task.id, element)" type="file" class="visually-hidden"
                  tabindex="-1" @change="event => selectFile(task.id, event)" />
              </template>
              <t-tooltip v-if="uploadTaskCanCancel(task.status)" :content="t('knowledgeBase.uploadQueue.cancel')">
                <button type="button" class="queue-icon-btn danger" :aria-label="t('knowledgeBase.uploadQueue.cancel')"
                  @click="queue.cancel(task.id)"><t-icon name="close-circle" aria-hidden="true" /></button>
              </t-tooltip>
              <t-tooltip v-if="uploadTaskCanRemove(task.status)" :content="t('knowledgeBase.uploadQueue.remove')">
                <button type="button" class="queue-icon-btn" :aria-label="t('knowledgeBase.uploadQueue.remove')"
                  @click="queue.remove(task.id)"><t-icon name="delete" aria-hidden="true" /></button>
              </t-tooltip>
            </div>
          </div>
        </article>
        <div v-if="!tasks.length" class="upload-queue-empty">{{ t('knowledgeBase.uploadQueue.empty') }}</div>
      </div>
    </section>
  </div>
</template>

<script setup lang="ts">
import { computed, watch } from 'vue'
import { useRoute } from 'vue-router'
import { storeToRefs } from 'pinia'
import { MessagePlugin } from 'tdesign-vue-next'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import { useUploadQueueStore, type UploadQueueStatus } from '@/stores/uploadQueue'
import {
  isKnowledgeBaseUploadRoute,
  uploadTaskCanCancel,
  uploadTaskCanRemove,
  uploadTaskCanRetry,
} from './uploadQueuePresentation'

const { t } = useI18n()
const authStore = useAuthStore()
const route = useRoute()
const queue = useUploadQueueStore()
const { tasks, activeCount, unfinishedCount } = storeToRefs(queue)
const orderedTasks = computed(() => [...tasks.value].sort((a, b) => b.createdAt - a.createdAt))
const fileInputs = new Map<string, HTMLInputElement>()

watch([() => authStore.isLoggedIn, () => authStore.effectiveTenantId], ([loggedIn]) => {
  if (loggedIn) queue.hydrate()
}, { immediate: true })

const progress = (task: { displayBytes: number; fileSize: number }) =>
  task.fileSize > 0 ? Math.min(100, Math.floor(task.displayBytes / task.fileSize * 100)) : 0

const formatBytes = (value: number) => {
  if (value < 1024) return `${Math.max(0, Math.round(value))} B`
  const units = ['KB', 'MB', 'GB', 'TB']
  let size = value / 1024
  let index = 0
  while (size >= 1024 && index < units.length - 1) { size /= 1024; index++ }
  return `${size >= 10 ? size.toFixed(1) : size.toFixed(2)} ${units[index]}`
}

const formatDuration = (seconds: number) => {
  if (seconds < 60) return t('knowledgeBase.uploadQueue.secondsLeft', { count: seconds }) as string
  if (seconds < 3600) return t('knowledgeBase.uploadQueue.minutesLeft', { count: Math.ceil(seconds / 60) }) as string
  return t('knowledgeBase.uploadQueue.hoursLeft', { count: (seconds / 3600).toFixed(1) }) as string
}

const statusLabel = (status: UploadQueueStatus) => t(`knowledgeBase.uploadQueue.status.${status}`) as string

const resume = (id: string) => {
  try { queue.resume(id) } catch (error: any) { MessagePlugin.error(error?.message) }
}

const setFileInput = (id: string, element: unknown) => {
  if (element instanceof HTMLInputElement) fileInputs.set(id, element)
  else fileInputs.delete(id)
}

const openFilePicker = (id: string) => fileInputs.get(id)?.click()

const selectFile = (id: string, event: Event) => {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  if (!file) return
  try { queue.resume(id, file) } catch (error: any) { MessagePlugin.error(error?.message) }
  input.value = ''
}
</script>

<style scoped>
.upload-queue-host { position: fixed; right: 22px; bottom: 22px; z-index: 2800; }
.upload-queue-trigger, .queue-icon-btn, .queue-file-picker { border: 0; background: transparent; color: var(--td-text-color-secondary); cursor: pointer; display: inline-flex; align-items: center; justify-content: center; }
.upload-queue-trigger { position: relative; width: 44px; height: 44px; border-radius: 50%; color: #fff; background: var(--td-brand-color); box-shadow: 0 8px 24px rgba(0,0,0,.18); }
.upload-queue-badge { position: absolute; top: -5px; right: -5px; min-width: 18px; height: 18px; padding: 0 4px; border-radius: 9px; color: #fff; background: var(--td-error-color); font-size: 11px; line-height: 18px; }
.upload-queue-panel { position: absolute; right: 0; bottom: 54px; width: min(430px, calc(100vw - 24px)); max-height: min(620px, calc(100vh - 100px)); display: flex; flex-direction: column; overflow: hidden; border: 1px solid var(--td-component-stroke); border-radius: 8px; background: var(--td-bg-color-container); box-shadow: 0 12px 36px rgba(0,0,0,.2); }
.upload-queue-header { display: flex; align-items: center; justify-content: space-between; padding: 14px 16px; border-bottom: 1px solid var(--td-component-stroke); }
.upload-queue-header div { display: flex; flex-direction: column; gap: 2px; }
.upload-queue-header span { color: var(--td-text-color-placeholder); font-size: 12px; }
.upload-queue-list { overflow-y: auto; padding: 8px; }
.upload-queue-item { padding: 10px; border-bottom: 1px solid var(--td-component-stroke); }
.upload-item-main { display: grid; grid-template-columns: 24px minmax(0,1fr) 42px; align-items: center; gap: 8px; }
.upload-file-icon { color: var(--td-brand-color); }
.upload-file-title { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 500; }
.upload-file-meta, .upload-file-detail { display: flex; gap: 8px; min-width: 0; color: var(--td-text-color-placeholder); font-size: 12px; }
.upload-file-percent { text-align: right; font-variant-numeric: tabular-nums; }
.upload-progress-track { height: 4px; margin: 9px 0; overflow: hidden; border-radius: 2px; background: var(--td-bg-color-secondarycontainer); }
.upload-progress-value { height: 100%; background: var(--td-brand-color); transition: width .2s linear; }
.upload-progress-value.failed { background: var(--td-error-color); }
.upload-item-footer { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
.upload-file-detail { overflow: hidden; }
.upload-file-error { overflow: hidden; color: var(--td-error-color); text-overflow: ellipsis; white-space: nowrap; }
.upload-item-actions { display: flex; flex: 0 0 auto; gap: 2px; }
.queue-icon-btn, .queue-file-picker { width: 28px; height: 28px; border-radius: 4px; }
.queue-icon-btn:hover, .queue-file-picker:hover { color: var(--td-brand-color); background: var(--td-bg-color-container-hover); }
.queue-icon-btn.danger:hover { color: var(--td-error-color); }
.upload-queue-trigger:focus-visible, .queue-icon-btn:focus-visible, .queue-file-picker:focus-visible { outline: 2px solid var(--td-brand-color); outline-offset: 2px; }
.upload-file-notice { overflow: hidden; color: var(--td-warning-color); text-overflow: ellipsis; white-space: nowrap; }
.visually-hidden { position: fixed; width: 1px; height: 1px; padding: 0; margin: -1px; overflow: hidden; clip: rect(0 0 0 0); white-space: nowrap; border: 0; }
.upload-queue-empty { padding: 28px; text-align: center; color: var(--td-text-color-placeholder); }
@media (max-width: 600px) { .upload-queue-host { right: 12px; bottom: 12px; } .upload-queue-panel { width: calc(100vw - 24px); } }
</style>
