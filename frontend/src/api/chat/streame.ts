import { fetchEventSource } from '@microsoft/fetch-event-source';
import { ref, onUnmounted } from 'vue';
import { generateRandomString } from '@/utils/index';
import i18n from '@/i18n';
import { getApiBaseUrl } from '@/utils/api-base';
import {
  sanitizeStreamRequestBody,
  type StreamRequestMeta,
} from '@/utils/chatRequestDebug';

const MAX_ERROR_BODY_BYTES = 16 * 1024;
const MAX_ERROR_MESSAGE_CHARS = 2048;

type FetchEventSource = typeof fetchEventSource;

interface UseStreamDependencies {
  fetchEventSource?: FetchEventSource;
}

interface StreamEventPayload {
  response_type?: unknown;
  type?: unknown;
  done?: unknown;
}

const isAbortError = (error: unknown): boolean =>
  error instanceof Error && error.name === 'AbortError';

const normalizeErrorMessage = (value: unknown): string => {
  if (typeof value !== 'string') return '';
  const normalized = value.replace(/\s+/g, ' ').trim();
  if (normalized.length <= MAX_ERROR_MESSAGE_CHARS) return normalized;
  return `${normalized.slice(0, MAX_ERROR_MESSAGE_CHARS)}...`;
};

const extractJSONErrorMessage = (value: unknown): string => {
  if (typeof value === 'string') return normalizeErrorMessage(value);
  if (!value || typeof value !== 'object' || Array.isArray(value)) return '';

  const record = value as Record<string, unknown>;
  for (const key of ['message', 'detail', 'error', 'content']) {
    const nested = record[key];
    const message = typeof nested === 'object'
      ? extractJSONErrorMessage(nested)
      : normalizeErrorMessage(nested);
    if (message) return message;
  }
  return '';
};

export async function readBoundedResponseText(
  response: Response,
  maxBytes = MAX_ERROR_BODY_BYTES,
): Promise<string> {
  if (!response.body || maxBytes <= 0) return '';

  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let bytesRead = 0;
  let text = '';
  let reachedLimit = false;

  try {
    while (bytesRead < maxBytes) {
      const { done, value } = await reader.read();
      if (done) break;

      const remaining = maxBytes - bytesRead;
      const chunk = value.byteLength > remaining ? value.subarray(0, remaining) : value;
      bytesRead += chunk.byteLength;
      text += decoder.decode(chunk, { stream: true });
      if (value.byteLength > remaining || bytesRead >= maxBytes) {
        reachedLimit = true;
        break;
      }
    }
    text += decoder.decode();
  } finally {
    if (reachedLimit) {
      await reader.cancel().catch(() => undefined);
    }
    reader.releaseLock();
  }

  return text;
}

export async function buildStreamHTTPErrorMessage(response: Response): Promise<string> {
  const rawBody = await readBoundedResponseText(response);
  let detail = '';

  if (rawBody.trim()) {
    try {
      detail = extractJSONErrorMessage(JSON.parse(rawBody));
    } catch {
      detail = normalizeErrorMessage(rawBody);
    }
  }

  const status = `HTTP ${response.status}`;
  const fallback = normalizeErrorMessage(response.statusText);
  return detail ? `${status}: ${detail}` : fallback ? `${status}: ${fallback}` : status;
}

export function isTerminalStreamEvent(data: unknown): boolean {
  if (typeof data === 'string' && data.trim() === '[DONE]') return true;
  if (!data || typeof data !== 'object' || Array.isArray(data)) return false;

  const payload = data as StreamEventPayload;
  const responseType = payload.response_type ?? payload.type;
  return responseType === 'complete'
    || ((responseType === 'error' || responseType === 'stop') && payload.done === true);
}

export function useStream(dependencies: UseStreamDependencies = {}) {
  const requestEventSource = dependencies.fetchEventSource ?? fetchEventSource;
  // 响应式状态
  const output = ref('')              // 显示内容
  const isStreaming = ref(false)      // 流状态
  const isLoading = ref(false)        // 初始加载
  const error = ref<string | null>(null)// 错误信息
  const lastStreamRequest = ref<StreamRequestMeta | null>(null)
  let activeController: AbortController | null = null
  let streamGeneration = 0

  const finishGeneration = (generation: number, abort: boolean) => {
    if (generation !== streamGeneration) return
    const controller = activeController
    activeController = null
    if (abort && controller && !controller.signal.aborted) controller.abort()
    isStreaming.value = false
    isLoading.value = false
  }

  // 启动流式请求
  const startStream = async (params: { session_id: any; query: any; knowledge_base_ids?: string[]; knowledge_ids?: string[]; tag_ids?: string[]; agent_enabled?: boolean; agent_id?: string; agent_source_tenant_id?: string | number; web_search_enabled?: boolean; summary_model_id?: string; mcp_service_ids?: string[]; skill_names?: string[]; mentioned_items?: Array<{id: string; name: string; type: string; kb_type?: string; kb_id?: string; kb_name?: string; service_id?: string; skill_name?: string}>; images?: Array<{data: string}>; attachment_uploads?: Array<{data: string; file_name: string; file_size: number}>; attachment_ids?: string[]; suggestion_attribution?: { suggestion_set_id: string; question_id: string }; method: string; url: string; embed_token?: string; embed_session_sig?: string; embed_visitor_id?: string }) => {
    const myGeneration = ++streamGeneration
    activeController?.abort()
    const requestController = new AbortController()
    activeController = requestController
    // 重置状态
    output.value = '';
    error.value = null;
    isStreaming.value = true;
    isLoading.value = true;

    // 获取API配置
    const apiUrl = getApiBaseUrl();
    
    const embedToken = params.embed_token;
    const token = embedToken || localStorage.getItem('weknora_token');
    if (!token) {
      error.value = i18n.global.t('error.tokenNotFound');
      finishGeneration(myGeneration, true);
      return;
    }

    // 跨空间访问请求头：只要 setSelectedTenant 写过激活空间，就附
    // X-Tenant-ID。早期版本会 short-circuit "selectedTenantId ===
    // defaultTenantId 时不附" 来减少 header 体积，但任何把 weknora_tenant
    // 写成激活空间的代码（OIDC 同步 / UserMenu loadUserInfo / router
    // hydrate）都会让两者相等，使得后续流式请求悄悄丢 header、落到
    // home 空间上，导致 SSE 接口返回 404。直接附即可——后端
    // IsTenantAccessible 也允许 header 指向自家空间。
    const selectedTenantId = localStorage.getItem('weknora_selected_tenant_id');
    const tenantIdHeader: string | null = selectedTenantId || null;

    // TTFB instrumentation: record the moment we kick off the request so
    // we can compare it with the first answer chunk we receive from the
    // server. This makes it possible to correlate the frontend-observed
    // latency with the backend "TTFB:first_answer_chunk" log line by
    // matching on X-Request-ID.
    const sentAt = performance.now();
    const requestID = generateRandomString(12);
    let firstAnswerLogged = false;
    let terminalReceived = false;

    try {
      const url =
        params.method == "POST"
          ? `${apiUrl}${params.url}/${params.session_id}`
          : `${apiUrl}${params.url}/${params.session_id}?message_id=${params.query}`;
      console.log(`[TTFB] request:start request_id=${requestID} url=${url} sent_at=${Date.now()}`);
      
      // Prepare POST body with required fields for agent-chat
      // knowledge_base_ids array and agent_enabled can update Session's SessionAgentConfig
      const postBody: any = { 
        query: params.query,
        agent_enabled: params.agent_enabled !== undefined ? params.agent_enabled : true
      };
      // Always include knowledge_base_ids for agent-chat (already validated above)
      if (params.knowledge_base_ids !== undefined && params.knowledge_base_ids.length > 0) {
        postBody.knowledge_base_ids = params.knowledge_base_ids;
      }
      // Include knowledge_ids if provided
      if (params.knowledge_ids !== undefined && params.knowledge_ids.length > 0) {
        postBody.knowledge_ids = params.knowledge_ids;
      }
      // Include agent_id if provided (backend resolves shared agent and tenant from share relation)
      if (params.agent_id) {
        postBody.agent_id = params.agent_id;
      }
      if (params.agent_source_tenant_id) {
        postBody.agent_source_tenant_id = Number(params.agent_source_tenant_id);
      }
      // Include web_search_enabled if provided
      if (params.web_search_enabled !== undefined) {
        postBody.web_search_enabled = params.web_search_enabled;
      }
      // Include summary_model_id if provided (for non-Agent mode)
      if (params.summary_model_id) {
        postBody.summary_model_id = params.summary_model_id;
      }
      // Include mcp_service_ids if provided (for Agent mode)
      if (params.mcp_service_ids !== undefined && params.mcp_service_ids.length > 0) {
        postBody.mcp_service_ids = params.mcp_service_ids;
      }
      if (params.skill_names !== undefined && params.skill_names.length > 0) {
        postBody.skill_names = params.skill_names;
      }
      if (params.tag_ids !== undefined && params.tag_ids.length > 0) {
        postBody.tag_ids = params.tag_ids;
      }
      // Include mentioned_items if provided (for displaying @mentions in chat)
      if (params.mentioned_items !== undefined && params.mentioned_items.length > 0) {
        postBody.mentioned_items = params.mentioned_items;
      }
      // Include images if provided (base64 data URIs for multimodal chat)
      if (params.images !== undefined && params.images.length > 0) {
        postBody.images = params.images;
      }
      // Include attachment_uploads if provided (documents, audio, etc.)
      if (params.attachment_uploads !== undefined && params.attachment_uploads.length > 0) {
        postBody.attachment_uploads = params.attachment_uploads;
      }
	  if (params.attachment_ids !== undefined && params.attachment_ids.length > 0) {
		postBody.attachment_ids = params.attachment_ids;
	  }
      if (params.suggestion_attribution) {
        postBody.suggestion_attribution = params.suggestion_attribution;
      }
      postBody.channel = embedToken ? "embed" : "web";

      lastStreamRequest.value = {
        requestId: requestID,
        url,
        method: params.method,
        body: params.method === 'POST' ? sanitizeStreamRequestBody(postBody) : null,
        sentAt: Date.now(),
      };
      
      await requestEventSource(url, {
        method: params.method,
        headers: {
          "Content-Type": "application/json",
          "Authorization": embedToken ? `Embed ${embedToken}` : `Bearer ${token}`,
          "Accept-Language": i18n.global.locale?.value || localStorage.getItem('locale') || 'zh-CN',
          "X-Request-ID": requestID,
          ...(!embedToken && tenantIdHeader ? { "X-Tenant-ID": tenantIdHeader } : {}),
          ...(params.embed_session_sig ? { "X-Embed-Session": params.embed_session_sig } : {}),
          ...(params.embed_visitor_id ? { "X-Embed-Visitor": params.embed_visitor_id } : {}),
        },
        body:
          params.method == "POST"
            ? JSON.stringify(postBody)
            : null,
        signal: requestController.signal,
        openWhenHidden: true,

        onopen: async (res) => {
          if (myGeneration !== streamGeneration || requestController.signal.aborted) return;
          if (!res.ok) throw new Error(await buildStreamHTTPErrorMessage(res));
          console.log(`[TTFB] response:headers request_id=${requestID} elapsed_ms=${(performance.now() - sentAt).toFixed(1)}`);
          isLoading.value = false;
        },

        onmessage: (ev) => {
          if (myGeneration !== streamGeneration || requestController.signal.aborted) return
          if (isTerminalStreamEvent(ev.data)) {
            terminalReceived = true;
            return;
          }

          let parsed: any;
          try {
            parsed = JSON.parse(ev.data);
          } catch {
            throw new Error(i18n.global.t('error.streamFailed'));
          }
          // Log first answer chunk for end-to-end TTFB measurement.
          // Filter by event type so non-answer events (references, tool
          // calls, etc.) don't count as the "first token" arrival.
          if (!firstAnswerLogged && (parsed?.response_type === 'answer' || parsed?.type === 'answer')) {
            firstAnswerLogged = true;
            console.log(`[TTFB] response:first_answer request_id=${requestID} elapsed_ms=${(performance.now() - sentAt).toFixed(1)}`);
          }
          // 执行自定义处理
          if (chunkHandler) {
            chunkHandler(parsed);
          }
          if (isTerminalStreamEvent(parsed)) {
            terminalReceived = true;
          }
        },

        onerror: (err) => {
          if (myGeneration !== streamGeneration || requestController.signal.aborted) return;
          throw err instanceof Error ? err : new Error(String(err));
        },

        onclose: () => {
          if (myGeneration !== streamGeneration || requestController.signal.aborted) return;
          if (!terminalReceived) throw new Error(i18n.global.t('error.streamFailed'));
          finishGeneration(myGeneration, false);
        },
      });

      if (myGeneration === streamGeneration && !requestController.signal.aborted) {
        if (!terminalReceived) throw new Error(i18n.global.t('error.streamFailed'));
        finishGeneration(myGeneration, false);
      }
    } catch (err) {
      if (
        myGeneration !== streamGeneration
        || requestController.signal.aborted
        || isAbortError(err)
      ) {
        return
      }
      error.value = err instanceof Error ? err.message : String(err)
      finishGeneration(myGeneration, true)
    }
  }

  let chunkHandler: ((data: any) => void) | null = null
  // 注册块处理器
  const onChunk = (handler: (data: any) => void) => {
    chunkHandler = handler
  }


  // 停止流
  const stopStream = () => {
    streamGeneration++
    const controller = activeController
    activeController = null
    if (controller && !controller.signal.aborted) controller.abort()
    isStreaming.value = false;
    isLoading.value = false;
  }

  // 组件卸载时自动清理
  onUnmounted(stopStream)

  return {
    output,          // 显示内容
    isStreaming,     // 是否在流式传输中
    isLoading,       // 初始连接状态
    error,
    lastStreamRequest,
    onChunk,
    startStream,     // 启动流
    stopStream       // 手动停止
  }
}
