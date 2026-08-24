<template>
  <IntegrationLandingLayout
    :title="$t('integrations.claw.title')"
    :subtitle="$t('integrations.claw.subtitle')"
    variant="claw"
  >
    <template #tags>
      <span v-for="key in compatibilityKeys" :key="key" class="scenario-tag">
        {{ $t(`integrations.claw.compatibility.${key}`) }}
      </span>
    </template>

    <template #actions>
      <IntegrationExternalCta
        variant="claw"
        :label="$t('integrations.claw.downloadCta')"
        :hint="$t('integrations.claw.downloadCtaHint')"
        :href="JIWAI_KNOWLEDGE_SKILL_DOWNLOAD_URL"
        download="见外知识库.zip"
        trailing-icon="download"
      >
        <template #icon>
          <t-icon name="download" size="18px" />
        </template>
      </IntegrationExternalCta>
    </template>

    <template #main>
      <div class="landing-group">
        <section class="setting-drawer__section">
          <h4 class="setting-drawer__section-title">{{ $t('integrations.claw.apiKeyTitle') }}</h4>
          <p class="field-desc">{{ $t('integrations.claw.apiKeyDesc') }}</p>

          <div v-if="apiKeysError" class="key-state key-state--error" role="alert">
            <t-icon name="error-circle" class="key-state__icon" />
            <div class="key-state__body">
              <p>{{ apiKeysError }}</p>
              <div class="key-state__actions">
                <t-button size="small" variant="outline" @click="loadAPIKeys">
                  <template #icon><t-icon name="refresh" /></template>
                  {{ $t('integrations.claw.retry') }}
                </t-button>
                <t-button size="small" variant="text" @click="openApiSettings">
                  {{ $t('integrations.claw.openApiSettings') }}
                </t-button>
              </div>
            </div>
          </div>

          <div v-else-if="apiKeysLoaded && availableAPIKeys.length === 0" class="key-state">
            <t-icon name="secured" class="key-state__icon" />
            <div class="key-state__body">
              <p>{{ $t('integrations.claw.noApiKeys') }}</p>
              <t-button size="small" variant="outline" @click="openApiSettings">
                {{ $t('integrations.claw.createApiKey') }}
              </t-button>
            </div>
          </div>

          <div v-else class="key-picker">
            <t-select
              v-model="selectedApiKeyId"
              class="key-picker__select"
              :loading="apiKeysLoading"
              :options="apiKeyOptions"
              :placeholder="$t('integrations.claw.apiKeyPlaceholder')"
            />
            <div v-if="selectedAPIKey" class="selected-key-summary">
              <div class="selected-key-summary__identity">
                <span class="selected-key-summary__name">{{ selectedAPIKey.name }}</span>
                <code>{{ maskAPIKey(selectedAPIKey.api_key) }}</code>
              </div>
              <span class="selected-key-summary__scope">{{ selectedAPIKeyScope }}</span>
            </div>
            <p v-if="expiredAPIKeyCount > 0" class="field-desc">
              {{ $t('integrations.claw.expiredKeysIgnored', { count: expiredAPIKeyCount }) }}
            </p>
          </div>
        </section>

        <section class="setting-drawer__section">
          <h4 class="setting-drawer__section-title">{{ $t('integrations.claw.promptTitle') }}</h4>
          <p class="field-desc">{{ $t('integrations.claw.promptDesc') }}</p>
          <div class="code-toolbar prompt-toolbar">
            <pre class="code-toolbar__code">{{ setupPromptPreview }}</pre>
            <t-button
              class="code-toolbar__copy"
              size="small"
              variant="text"
              shape="square"
              :disabled="!selectedAPIKey"
              :title="$t('integrations.claw.copyPrompt')"
              @click="copySetupPrompt"
            >
              <t-icon name="file-copy" size="16px" />
            </t-button>
          </div>
          <div class="security-note" role="note">
            <t-icon name="secured" />
            <p>{{ $t('integrations.claw.securityNote') }}</p>
          </div>
          <t-button
            theme="primary"
            :disabled="!selectedAPIKey"
            class="copy-prompt-button"
            @click="copySetupPrompt"
          >
            <template #icon><t-icon name="file-copy" /></template>
            {{ $t('integrations.claw.copyPrompt') }}
          </t-button>
        </section>
      </div>
    </template>

    <template #aside>
      <div class="landing-group">
        <section class="setting-drawer__section">
          <h4 class="setting-drawer__section-title">{{ $t('integrations.claw.stepsTitle') }}</h4>
          <ol class="landing-steps">
            <li v-for="(step, index) in stepKeys" :key="step" class="landing-step">
              <span class="landing-step-num">{{ index + 1 }}</span>
              <div class="landing-step-body">
                <div class="landing-step-title">{{ $t(`integrations.claw.steps.${step}.title`) }}</div>
                <p class="landing-step-desc">{{ $t(`integrations.claw.steps.${step}.desc`) }}</p>
              </div>
            </li>
          </ol>
        </section>

        <section class="setting-drawer__section">
          <h4 class="setting-drawer__section-title">
            {{ $t('integrations.claw.capabilitiesTitle') }}
            <span class="section-head-extra">{{ capabilityKeys.length }}</span>
          </h4>
          <ul class="compact-capability-list">
            <li v-for="key in capabilityKeys" :key="key">
              <span class="compact-capability-list__icon">
                <t-icon :name="capabilityIcons[key]" />
              </span>
              <span>
                <strong>{{ $t(`integrations.claw.capabilities.${key}.title`) }}</strong>
                <small>{{ $t(`integrations.claw.capabilities.${key}.desc`) }}</small>
              </span>
            </li>
          </ul>
        </section>
      </div>
    </template>

    <template #footer>
      <div class="landing-footer-bar landing-footer-bar--inline" role="note">
        <div>
          <p class="landing-footer-bar__note">{{ $t('integrations.claw.ecosystemNote') }}</p>
          <span class="landing-meta">{{ $t('integrations.claw.packageMeta') }}</span>
        </div>
        <t-button size="small" variant="text" @click="openClawHub">
          <template #icon><t-icon name="link" /></template>
          {{ $t('integrations.claw.openClawHub') }}
        </t-button>
      </div>
    </template>
  </IntegrationLandingLayout>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { MessagePlugin } from 'tdesign-vue-next'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { listTenantAPIKeys, type TenantAPIKey } from '@/api/tenant'
import {
  CLAWHUB_SKILL_URL,
  JIWAI_KNOWLEDGE_SKILL_ARCHIVE_NAME,
  JIWAI_KNOWLEDGE_SKILL_DOWNLOAD_URL,
  OFFICECLI_SKILL_INSTALL_COMMAND,
} from '@/config/integrations'
import { useApiBaseUrlDisplay } from '@/composables/useApiBaseUrlDisplay'
import { useAuthStore } from '@/stores/auth'
import { useUIStore } from '@/stores/ui'
import { copyWithToast } from '@/utils/clipboard'
import IntegrationExternalCta from './IntegrationExternalCta.vue'
import IntegrationLandingLayout from './IntegrationLandingLayout.vue'

const router = useRouter()
const authStore = useAuthStore()
const uiStore = useUIStore()
const { t } = useI18n()
const { apiBaseUrlDisplay } = useApiBaseUrlDisplay()

const capabilityKeys = ['upload', 'url', 'manual', 'search', 'browse'] as const
const compatibilityKeys = ['codex', 'claude', 'cursor', 'openclaw', 'generic'] as const
const stepKeys = ['download', 'key', 'prompt', 'verify'] as const

const capabilityIcons: Record<(typeof capabilityKeys)[number], string> = {
  upload: 'upload',
  url: 'link',
  manual: 'edit',
  search: 'search',
  browse: 'view-list',
}

const apiKeys = ref<TenantAPIKey[]>([])
const apiKeysLoading = ref(false)
const apiKeysLoaded = ref(false)
const apiKeysError = ref('')
const selectedApiKeyId = ref<number>()
let apiKeyRequestSerial = 0

const activeTenantId = computed(() => Number(authStore.effectiveTenantId || 0))
const availableAPIKeys = computed(() => apiKeys.value.filter((key) => key.api_key && !isAPIKeyExpired(key)))
const expiredAPIKeyCount = computed(() => apiKeys.value.filter(isAPIKeyExpired).length)
const selectedAPIKey = computed(() => availableAPIKeys.value.find((key) => key.id === selectedApiKeyId.value))
const selectedAPIKeyScope = computed(() => (
  selectedAPIKey.value?.full_access
    ? t('integrations.claw.apiKeyFullAccess')
    : t('integrations.claw.apiKeyScopedAccess')
))
const apiKeyOptions = computed(() => availableAPIKeys.value.map((key) => ({
  value: key.id,
  label: `${key.name} · ${maskAPIKey(key.api_key)}`,
})))

const setupPrompt = computed(() => {
  if (!selectedAPIKey.value) return ''
  return buildSetupPrompt(selectedAPIKey.value.api_key)
})

const setupPromptPreview = computed(() => {
  const previewKey = selectedAPIKey.value
    ? maskAPIKey(selectedAPIKey.value.api_key)
    : t('integrations.claw.apiKeyPromptPlaceholder')
  return buildSetupPrompt(previewKey)
})

function buildSetupPrompt(apiKey: string) {
  return t('integrations.claw.setupPrompt', {
    archiveName: JIWAI_KNOWLEDGE_SKILL_ARCHIVE_NAME,
    baseUrl: apiBaseUrlDisplay.value || 'https://your-server.com/api/v1',
    apiKey,
    officeCliInstallCommand: OFFICECLI_SKILL_INSTALL_COMMAND,
  })
}

function maskAPIKey(value: string) {
  if (!value) return '-'
  if (value.length <= 12) return '*'.repeat(value.length)
  return `${value.slice(0, 8)}${'*'.repeat(8)}${value.slice(-6)}`
}

function isAPIKeyExpired(key: TenantAPIKey) {
  if (!key.expires_at) return false
  const expiresAt = Date.parse(key.expires_at)
  return !Number.isNaN(expiresAt) && expiresAt <= Date.now()
}

async function loadAPIKeys() {
  const tenantId = activeTenantId.value
  const requestSerial = ++apiKeyRequestSerial
  apiKeysError.value = ''
  apiKeysLoaded.value = false

  if (!tenantId) {
    apiKeys.value = []
    selectedApiKeyId.value = undefined
    apiKeysError.value = t('integrations.claw.tenantUnavailable')
    apiKeysLoaded.value = true
    return
  }

  apiKeysLoading.value = true
  try {
    const response = await listTenantAPIKeys(tenantId)
    if (requestSerial !== apiKeyRequestSerial) return
    if (!response.success) {
      throw new Error(response.message || t('integrations.claw.loadApiKeysFailed'))
    }

    apiKeys.value = response.data || []
    const currentSelectionExists = availableAPIKeys.value.some((key) => key.id === selectedApiKeyId.value)
    if (!currentSelectionExists) {
      selectedApiKeyId.value = availableAPIKeys.value[0]?.id
    }
  } catch (error: unknown) {
    if (requestSerial !== apiKeyRequestSerial) return
    apiKeys.value = []
    selectedApiKeyId.value = undefined
    apiKeysError.value = error instanceof Error
      ? error.message
      : t('integrations.claw.loadApiKeysFailed')
  } finally {
    if (requestSerial === apiKeyRequestSerial) {
      apiKeysLoading.value = false
      apiKeysLoaded.value = true
    }
  }
}

function openClawHub() {
  window.open(CLAWHUB_SKILL_URL, '_blank', 'noopener,noreferrer')
}

function openApiSettings() {
  router.push({ path: '/platform/settings', query: { section: 'integrations', tab: 'api' } })
  uiStore.openSettings('integration-api')
}

async function copySetupPrompt() {
  if (!setupPrompt.value) {
    MessagePlugin.warning(t('integrations.claw.selectApiKeyFirst'))
    return
  }
  await copyWithToast(setupPrompt.value, 'integrations.claw.copyPromptSuccess')
}

watch(activeTenantId, loadAPIKeys, { immediate: true })
</script>
