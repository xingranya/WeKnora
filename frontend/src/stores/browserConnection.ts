import { defineStore } from 'pinia'
import { get } from '@/utils/request'

export type BrowserAccountStatus = {
  enabled: boolean
  connected: boolean
  device?: { id: string; label: string; last_seen_at: string }
  extension_available: boolean
}

type BrowserConnectionState = BrowserAccountStatus & {
  loaded: boolean
  subscribers: number
  revision: number
}

let pollTimer: ReturnType<typeof setTimeout> | undefined
let pollController: AbortController | undefined
let pollingController: AbortController | undefined
let visibilityHandler: (() => void) | undefined

export const useBrowserConnectionStore = defineStore('browserConnection', {
  state: (): BrowserConnectionState => ({
    enabled: false,
    connected: false,
    extension_available: false,
    loaded: false,
    subscribers: 0,
    revision: 0,
  }),
  getters: {
    online: (state) => state.enabled && state.connected,
    knownOffline: (state) => state.loaded && !(state.enabled && state.connected),
  },
  actions: {
    apply(data: BrowserAccountStatus) {
      this.revision += 1
      this.enabled = data.enabled === true
      this.connected = data.connected === true
      this.device = data.device
      this.extension_available = data.extension_available === true
      this.loaded = true
    },
    async refresh() {
      const revision = ++this.revision
      const controller = pollController
      const result = await get<{ data: BrowserAccountStatus }>('/api/v1/me/browser', {
        signal: controller?.signal,
      })
      if (revision !== this.revision || controller?.signal.aborted) return
      this.apply(result.data)
    },
    watchStatus() {
      this.subscribers += 1
      if (this.subscribers !== 1) return
      pollController = new AbortController()
      visibilityHandler = () => {
        if (!document.hidden) {
          clearTimeout(pollTimer)
          void this.poll()
        }
      }
      document.addEventListener('visibilitychange', visibilityHandler)
      void this.poll()
    },
    unwatchStatus() {
      this.subscribers = Math.max(0, this.subscribers - 1)
      if (this.subscribers !== 0) return
      if (visibilityHandler) {
        document.removeEventListener('visibilitychange', visibilityHandler)
        visibilityHandler = undefined
      }
      pollController?.abort()
      pollController = undefined
      clearTimeout(pollTimer)
      pollTimer = undefined
      // 最后一个页面离开时丢弃账号状态，重登或重新进入后重新读取。
      this.revision += 1
      this.enabled = false
      this.connected = false
      this.device = undefined
      this.extension_available = false
      this.loaded = false
    },
    async poll() {
      const controller = pollController
      if (!controller || this.subscribers === 0 || pollingController === controller) return
      pollingController = controller
      clearTimeout(pollTimer)
      try {
        if (!document.hidden) await this.refresh()
      } catch {
        // Keep the last known status; the composer stays conservative once loaded.
      }
      if (pollingController === controller) pollingController = undefined
      if (this.subscribers > 0 && pollController === controller) {
        pollTimer = setTimeout(() => { void this.poll() }, this.online ? 5000 : 3000)
      }
    },
  },
})
