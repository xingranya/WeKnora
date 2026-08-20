// background.js — Service Worker
// 存储管理 + 消息路由 + 右键菜单 + API 通信

importScripts('network.js', 'collection.js');

var COMPANY_API_BASE = JiwaiNetwork.COMPANY_API_BASE;

async function migrateToCompanyService() {
  var data = await chrome.storage.local.get(['ka_auth', 'ka_config']);
  var changes = {
    ka_config: {
      baseUrl: COMPANY_API_BASE,
      apiKey: (data.ka_config && data.ka_config.apiKey) || ''
    }
  };
  await chrome.storage.local.set(changes);
  await chrome.storage.local.remove('ka_chatbot_token');
  if (data.ka_auth && data.ka_auth.type !== 'wk') {
    await chrome.storage.local.remove('ka_auth');
  }
}

migrateToCompanyService();

// === WeKnora API Helper ===
// 构建请求头：API Key 使用 X-API-Key header，Bearer token 使用 Authorization header
async function buildHeaders(config) {
  var headers = {
    'Content-Type': 'application/json',
    'X-Request-ID': Date.now().toString(36) + Math.random().toString(36).slice(2, 8)
  };
  if (config.apiKey) {
    // API Key (sk- 开头) 使用 X-API-Key header 进行租户级认证
    headers['X-API-Key'] = config.apiKey;
  }
  if (config.bearerToken) {
    // Bearer token (通过用户名密码登录获取) 使用 Authorization header
    headers['Authorization'] = 'Bearer ' + config.bearerToken;
  }
  return headers;
}

async function apiRequest(method, path, body, options) {
  var config = await getConfigData();
  if (!config || !config.baseUrl) {
    return { success: false, error: '未配置服务地址，请先在设置中配置' };
  }
  var baseUrl = config.baseUrl.replace(/\/+$/, '');
  var url = baseUrl + path;
  var headers = await buildHeaders(config);
  try {
    var fetchOpts = { method: method, headers: headers };
    if (body && method !== 'GET') {
      fetchOpts.body = JSON.stringify(body);
    }
    if (options && options.signal) {
      fetchOpts.signal = options.signal;
    }
    var result = await JiwaiNetwork.requestTextWithTimeout(fetch, url, fetchOpts);
    var resp = result.response;
    var responseText = result.text;
    if (!resp.ok) {
      var errText = resp.statusText;
      try { var errJson = JSON.parse(responseText); errText = errJson.error?.message || errJson.message || errText; } catch (e) {}
      return JiwaiNetwork.httpFailure(resp.status, errText);
    }
    try { return JSON.parse(responseText); } catch (parseError) { return JiwaiNetwork.invalidResponseFailure(); }
  } catch (err) {
    return JiwaiNetwork.fetchFailure(err);
  }
}

// SSE streaming chat request — returns ReadableStream
async function apiChatStream(path, body) {
  var config = await getConfigData();
  if (!config || !config.baseUrl) {
    return { success: false, error: '未配置服务地址' };
  }
  var baseUrl = config.baseUrl.replace(/\/+$/, '');
  var url = baseUrl + path;
  var headers = await buildHeaders(config);
  headers['Accept'] = 'text/event-stream';
  try {
    var resp = await JiwaiNetwork.requestWithTimeout(fetch, url, {
      method: 'POST',
      headers: headers,
      body: JSON.stringify(body),
      cache: 'no-store'  // 避免浏览器缓存导致 SSE 流被缓冲
    });
    if (!resp.ok) {
      var errText = resp.statusText;
      try {
        var responseText = await JiwaiNetwork.readResponseTextWithTimeout(resp);
        var errJson = JSON.parse(responseText);
        errText = errJson.error?.message || errJson.message || errText;
      } catch (e) {
        if (e && e.code === 'REQUEST_TIMEOUT') return JiwaiNetwork.fetchFailure(e);
      }
      return JiwaiNetwork.httpFailure(resp.status, errText);
    }
    return { success: true, response: resp };
  } catch (err) {
    return JiwaiNetwork.fetchFailure(err);
  }
}

// 流式推送消息到 sidepanel / popup 等前端页面
function notifyStream(msg) {
  chrome.runtime.sendMessage(msg).catch(function () {});
}

// Helper to get raw config data
async function getConfigData() {
  var data = await chrome.storage.local.get('ka_config');
  var config = data.ka_config || {};
  return {
    baseUrl: COMPANY_API_BASE,
    apiKey: config.apiKey || ''
  };
}

function scoreFrameExtractionCandidate(candidate) {
  if (!candidate) return -Infinity;
  var score = Number(candidate.markdownLength) || 0;
  if (candidate.matchedSite) score += 100000;
  if (candidate.adapterId === 'feishu') score += 300000;
  if (candidate.incomplete) score -= 500000;
  return score;
}

async function waitForEmbeddedDocumentFrame(tabId) {
  try {
    var result = await chrome.scripting.executeScript({
      target: { tabId: tabId, frameIds: [0] },
      func: async function () {
        var host = document.querySelector('tt-docs-component');
        if (!host) return { hasEmbeddedDocument: false, frameReady: false };
        for (var attempt = 0; attempt < 32; attempt++) {
          var frame = host.shadowRoot && host.shadowRoot.querySelector('iframe');
          if (frame && /^https?:/i.test(frame.src || '')) {
            return { hasEmbeddedDocument: true, frameReady: true };
          }
          await new Promise(function (resolve) { setTimeout(resolve, 250); });
        }
        return { hasEmbeddedDocument: true, frameReady: false };
      }
    });
    return result[0] && result[0].result || { hasEmbeddedDocument: false, frameReady: false };
  } catch (error) {
    return { hasEmbeddedDocument: false, frameReady: false };
  }
}

async function runFrameExtractionPass(tabId) {
  await chrome.scripting.executeScript({
    target: { tabId: tabId, allFrames: true },
    files: ['extractors.js']
  });
  var injections = await chrome.scripting.executeScript({
    target: { tabId: tabId, allFrames: true },
    func: async function () {
      if (!window.JiwaiPageExtractor) return null;
      try {
        var result = await window.JiwaiPageExtractor.extract({
          document: document,
          url: location.href,
          maxWaitMs: 5000
        });
        window.__jiwaiFrameExtraction = result;
        return {
          title: result.title || '',
          author: result.author || '',
          description: result.description || '',
          site: result.site || '',
          published: result.published || '',
          matchedSite: !!result.matchedSite,
          adapterId: result.adapterId || 'generic',
          incomplete: !!result.incomplete,
          imageCount: result.imageCount || 0,
          blockCount: result.blockCount || 0,
          markdownLength: (result.markdown || '').length
        };
      } catch (error) {
        return null;
      }
    }
  });
  return injections.map(function (injection) {
    if (!injection.result || injection.result.markdownLength <= 80) return null;
    return Object.assign({ frameId: injection.frameId }, injection.result);
  }).filter(Boolean);
}

async function extractAllFrames(tabId) {
  try {
    var readiness = await waitForEmbeddedDocumentFrame(tabId);
    var attempts = readiness.hasEmbeddedDocument ? 3 : 1;
    var candidateByFrame = new Map();
    for (var attempt = 0; attempt < attempts; attempt++) {
      var passCandidates = await runFrameExtractionPass(tabId);
      passCandidates.forEach(function (candidate) {
        var existing = candidateByFrame.get(candidate.frameId);
        if (!existing || scoreFrameExtractionCandidate(candidate) > scoreFrameExtractionCandidate(existing)) {
          candidateByFrame.set(candidate.frameId, candidate);
        }
      });
      var completeEmbedded = passCandidates.some(function (candidate) {
        return candidate.adapterId === 'feishu' && !candidate.incomplete && candidate.markdownLength > 1000;
      });
      if (!readiness.hasEmbeddedDocument || completeEmbedded) break;
      await new Promise(function (resolve) { setTimeout(resolve, 900); });
    }

    var candidates = Array.from(candidateByFrame.values()).filter(function (candidate) {
      return !candidate.incomplete;
    });
    candidates.sort(function (left, right) {
      return scoreFrameExtractionCandidate(right) - scoreFrameExtractionCandidate(left);
    });
    var best = candidates[0];
    if (readiness.hasEmbeddedDocument && (!best || best.adapterId !== 'feishu')) {
      return { success: false, error: '内嵌飞书文档尚未加载完成，请保持页面打开后重试' };
    }
    if (!best) return { success: true, data: [] };

    return {
      success: true,
      data: [Object.assign({}, best, { chunked: true })]
    };
  } catch (error) {
    return { success: false, error: error.message || '跨 frame 页面读取失败' };
  }
}

async function readFrameExtractionChunk(tabId, frameId, offset, size) {
  var safeSize = Math.max(1, Math.min(Number(size) || 0, 512 * 1024));
  var safeOffset = Math.max(0, Number(offset) || 0);
  try {
    var result = await chrome.scripting.executeScript({
      target: { tabId: tabId, frameIds: [Number(frameId)] },
      func: function (chunkOffset, chunkSize) {
        var extraction = window.__jiwaiFrameExtraction;
        return extraction && extraction.markdown
          ? extraction.markdown.slice(chunkOffset, chunkOffset + chunkSize)
          : '';
      },
      args: [safeOffset, safeSize]
    });
    return { success: true, data: (result[0] && result[0].result) || '' };
  } catch (error) {
    return { success: false, error: error.message || '读取采集分块失败' };
  }
}

async function clearFrameExtraction(tabId, frameId) {
  try {
    await chrome.scripting.executeScript({
      target: { tabId: tabId, frameIds: [Number(frameId)] },
      func: function () { delete window.__jiwaiFrameExtraction; }
    });
  } catch (e) {}
  return { success: true };
}

// === 文档集采集 ===
var DOCUMENT_COLLECTION_TASK_KEY = 'ka_document_collection_task';
var DOCUMENT_COLLECTION_HISTORY_KEY = 'ka_document_collection_history';
var DOCUMENT_COLLECTION_ALARM = 'ka-document-collection-watchdog';
var DOCUMENT_COLLECTION_MAX_PAGES = 50;
var DOCUMENT_COLLECTION_MAX_ATTEMPTS = 2;
var DOCUMENT_COLLECTION_CHUNK_SIZE = 512 * 1024;

function collectionDelay(milliseconds) {
  return new Promise(function (resolve) { setTimeout(resolve, milliseconds); });
}

async function getDocumentCollectionTask() {
  var data = await chrome.storage.local.get(DOCUMENT_COLLECTION_TASK_KEY);
  return data[DOCUMENT_COLLECTION_TASK_KEY] || null;
}

function updateCollectionBadge(task) {
  if (!chrome.action || !chrome.action.setBadgeText) return;
  var badgeText = '';
  if (task && (task.status === 'running' || task.status === 'paused')) {
    badgeText = String((task.completed || 0) + (task.failed || 0) + (task.skipped || 0)) + '/' + String(task.total || 0);
  }
  chrome.action.setBadgeBackgroundColor({ color: task && task.status === 'paused' ? '#8A8F98' : '#07C160' }).catch(function () {});
  chrome.action.setBadgeText({ text: badgeText }).catch(function () {});
}

async function setDocumentCollectionTask(task) {
  task.updatedAt = Date.now();
  var payload = {};
  payload[DOCUMENT_COLLECTION_TASK_KEY] = task;
  await chrome.storage.local.set(payload);
  updateCollectionBadge(task);
  chrome.runtime.sendMessage({ type: 'DOCUMENT_COLLECTION_UPDATED', payload: task }).catch(function () {});
  return task;
}

function normalizeCollectionPages(pages) {
  var output = [];
  var seen = new Set();
  (pages || []).forEach(function (page) {
    if (output.length >= DOCUMENT_COLLECTION_MAX_PAGES) return;
    var url = globalThis.JiwaiCollection.canonicalizeUrl(page && page.url, page && page.url);
    if (!url || seen.has(url)) return;
    seen.add(url);
    output.push({
      url: url,
      title: String(page.title || '未命名文档').trim().slice(0, 240) || '未命名文档',
      navigationOnly: !!page.navigationOnly,
      status: 'pending',
      attempts: 0,
      error: ''
    });
  });
  return output;
}

function appendDiscoveredCollectionPages(task, pages) {
  var seen = new Set(task.pages.map(function (page) { return page.url; }));
  var added = 0;
  (pages || []).forEach(function (page) {
    if (task.pages.length >= DOCUMENT_COLLECTION_MAX_PAGES) return;
    var url = globalThis.JiwaiCollection.canonicalizeUrl(page && page.url, page && page.url);
    if (!url || seen.has(url)) return;
    seen.add(url);
    task.pages.push({
      url: url,
      title: String(page.title || '未命名文档').trim().slice(0, 240) || '未命名文档',
      navigationOnly: !!page.navigationOnly,
      status: 'pending',
      attempts: 0,
      error: ''
    });
    added++;
  });
  task.total = task.pages.length;
  return added;
}

async function discoverCollectionPagesInTab(tabId, maxPages) {
  try {
    await chrome.scripting.executeScript({
      target: { tabId: tabId, frameIds: [0] },
      files: ['collection.js'],
      world: 'MAIN'
    });
    var result = await chrome.scripting.executeScript({
      target: { tabId: tabId, frameIds: [0] },
      func: async function (maxPages) {
        if (!window.JiwaiCollection) return null;
        return window.JiwaiCollection.discoverDocumentLinks(document, location.href, { maxPages: maxPages });
      },
      args: [Math.max(1, Math.min(Number(maxPages) || DOCUMENT_COLLECTION_MAX_PAGES, DOCUMENT_COLLECTION_MAX_PAGES))],
      world: 'MAIN'
    });
    return result[0] && result[0].result || { pages: [], total: 0, truncated: false };
  } catch (error) {
    return { pages: [], total: 0, truncated: false, error: error.message || '识别文档目录失败' };
  }
}

async function createCollectionWorkerTab(task) {
  var tab = await chrome.tabs.create({ url: 'about:blank', active: false });
  task.workerTabId = tab.id;
  task.processing = false;
  task.processingStartedAt = 0;
  await setDocumentCollectionTask(task);
  await chrome.tabs.update(tab.id, { url: task.pages[task.currentIndex].url });
  return task;
}

async function startDocumentCollection(payload, sender) {
  payload = payload || {};
  if (!payload.kbId) return { success: false, error: '请先选择知识库' };
  var pages = normalizeCollectionPages(payload.pages);
  if (!pages.length) return { success: false, error: '未识别到可采集的文档页面' };

  var current = await getDocumentCollectionTask();
  if (current && (current.status === 'running' || current.status === 'paused')) {
    return { success: false, error: '已有文档集采集任务，请先完成或取消当前任务', data: current };
  }

  var task = {
    id: globalThis.JiwaiCollection.createTaskId(),
    title: String(payload.title || '文档集').trim().slice(0, 240),
    scope: String(payload.scope || ''),
    kbId: payload.kbId,
    kbName: String(payload.kbName || ''),
    sourceTabId: payload.sourceTabId || (sender && sender.tab && sender.tab.id) || null,
    workerTabId: null,
    status: 'running',
    pages: pages,
    currentIndex: 0,
    total: pages.length,
    completed: 0,
    failed: 0,
    skipped: 0,
    processing: false,
    processingStartedAt: 0,
    createdAt: Date.now(),
    updatedAt: Date.now(),
    error: ''
  };

  await setDocumentCollectionTask(task);
  await chrome.alarms.create(DOCUMENT_COLLECTION_ALARM, { delayInMinutes: 1, periodInMinutes: 1 });
  await createCollectionWorkerTab(task);
  return { success: true, data: task };
}

async function readCollectionExtraction(tabId, pageUrl) {
  var response = await extractAllFrames(tabId);
  if (!response || !response.success || !response.data || !response.data[0]) {
    throw new Error((response && response.error) || '未读取到页面正文');
  }
  var extraction = response.data[0];
  var parts = [];
  try {
    for (var offset = 0; offset < extraction.markdownLength; offset += DOCUMENT_COLLECTION_CHUNK_SIZE) {
      var chunk = await readFrameExtractionChunk(tabId, extraction.frameId, offset, DOCUMENT_COLLECTION_CHUNK_SIZE);
      if (!chunk || !chunk.success) throw new Error((chunk && chunk.error) || '读取正文分块失败');
      parts.push(chunk.data || '');
    }
  } finally {
    await clearFrameExtraction(tabId, extraction.frameId);
  }

  var markdown = parts.join('').trim();
  if (markdown.length < 40) throw new Error('页面正文过短，请确认页面已加载完成');
  if (markdown.length < 5000 && /(当前页面需要登录查看|登录后查看完整内容|请先登录后查看|验证码验证)/.test(markdown)) {
    throw new Error('页面需要登录或验证码验证，任务已暂停');
  }
  var title = String(extraction.title || '').replace(/\s*-\s*巨量[^-]*帮助中心\s*$/, '').trim() || '未命名文档';
  var sourceUrl = globalThis.JiwaiCollection.canonicalizeUrl(pageUrl, pageUrl) || pageUrl;
  var header = '';
  if (extraction.author) header += '> 作者: ' + extraction.author + '\n';
  if (extraction.site) header += '> 来源: [' + extraction.site + '](' + sourceUrl + ')\n';
  if (extraction.published) header += '> 发布时间: ' + extraction.published + '\n';
  if (header) header += '\n';
  return {
    title: title.replace(/\.+$/, ''),
    content: header + markdown + '\n\n---\n来源: ' + sourceUrl,
    url: sourceUrl,
    adapterId: extraction.adapterId || 'generic',
    imageCount: extraction.imageCount || 0,
    blockCount: extraction.blockCount || 0
  };
}

async function getCollectionHistory() {
  var data = await chrome.storage.local.get(DOCUMENT_COLLECTION_HISTORY_KEY);
  return data[DOCUMENT_COLLECTION_HISTORY_KEY] || {};
}

async function setCollectionHistory(history) {
  var entries = Object.entries(history).sort(function (left, right) {
    return (right[1].updatedAt || 0) - (left[1].updatedAt || 0);
  }).slice(0, 1000);
  var payload = {};
  payload[DOCUMENT_COLLECTION_HISTORY_KEY] = Object.fromEntries(entries);
  await chrome.storage.local.set(payload);
}

async function saveCollectionPage(task, page, extraction) {
  var markedContent = '<!-- weknora-clip-type:collection-clip -->\n' + extraction.content;
  var response = await autoApiRequest('POST', '/knowledge-bases/' + task.kbId + '/knowledge/manual', {
    title: extraction.title || page.title,
    content: markedContent,
    status: 'publish',
    channel: 'browser_extension'
  });
  if (!response || response.success === false || response.error) {
    throw new Error((response && response.error) || '写入知识库失败');
  }

  var knowledgeId = response.data && response.data.id || '';
  var stored = await chrome.storage.local.get('ka_clips');
  var clips = stored.ka_clips || [];
  clips.unshift({
    id: Date.now().toString(36) + Math.random().toString(36).slice(2, 6),
    type: 'collection-clip',
    title: extraction.title || page.title,
    content: extraction.content.slice(0, 4000),
    contentTruncated: extraction.content.length > 4000,
    createdAt: new Date().toISOString(),
    knowledgeId: knowledgeId,
    knowledgeBaseId: task.kbId,
    meta: {
      url: extraction.url,
      collectionId: task.id,
      collectionTitle: task.title,
      adapterId: extraction.adapterId,
      imageCount: extraction.imageCount,
      blockCount: extraction.blockCount
    }
  });
  await chrome.storage.local.set({ ka_clips: clips.slice(0, 200) });

  var history = await getCollectionHistory();
  var historyKey = task.kbId + '|' + extraction.url;
  history[historyKey] = {
    knowledgeId: knowledgeId,
    title: extraction.title || page.title,
    contentHash: globalThis.JiwaiCollection.simpleHash(extraction.content),
    updatedAt: Date.now()
  };
  await setCollectionHistory(history);
  return knowledgeId;
}

async function finishDocumentCollection(task) {
  task.status = task.failed > 0 && task.completed === 0 ? 'failed' : 'completed';
  task.processing = false;
  task.processingStartedAt = 0;
  task.completedAt = Date.now();
  await setDocumentCollectionTask(task);
  await chrome.alarms.clear(DOCUMENT_COLLECTION_ALARM);
  if (task.workerTabId) {
    try { await chrome.tabs.remove(task.workerTabId); } catch (error) {}
  }
  if (task.sourceTabId) {
    var summary = '文档集采集完成：成功 ' + task.completed + '，跳过 ' + task.skipped + '，失败 ' + task.failed;
    chrome.tabs.sendMessage(task.sourceTabId, {
      type: 'SHOW_NOTIFICATION',
      payload: { msg: summary, status: task.failed ? 'info' : 'success' }
    }).catch(function () {});
  }
  return task;
}

async function moveToNextCollectionPage(task) {
  task.currentIndex++;
  task.processing = false;
  task.processingStartedAt = 0;
  while (task.currentIndex < task.pages.length && task.pages[task.currentIndex].status !== 'pending') {
    task.currentIndex++;
  }
  if (task.currentIndex >= task.pages.length) return finishDocumentCollection(task);
  await setDocumentCollectionTask(task);
  if (task.status !== 'running') return task;
  await collectionDelay(1800);
  await chrome.tabs.update(task.workerTabId, { url: task.pages[task.currentIndex].url });
  return task;
}

async function processDocumentCollectionPage(sender, payload) {
  var task = await getDocumentCollectionTask();
  var tabId = sender && sender.tab && sender.tab.id;
  if (!task || task.status !== 'running' || !tabId || tabId !== task.workerTabId) {
    return { success: true, ignored: true };
  }
  if (task.processing && Date.now() - (task.processingStartedAt || 0) < 120000) {
    return { success: true, ignored: true };
  }

  var page = task.pages[task.currentIndex];
  if (!page) {
    await finishDocumentCollection(task);
    return { success: true };
  }

  task.processing = true;
  task.processingStartedAt = Date.now();
  page.status = 'processing';
  page.attempts++;
  page.error = '';
  await setDocumentCollectionTask(task);

  try {
    var loadedUrl = payload && payload.url || page.url;
    var nestedDiscovery = await discoverCollectionPagesInTab(tabId, DOCUMENT_COLLECTION_MAX_PAGES);
    if (appendDiscoveredCollectionPages(task, nestedDiscovery.pages)) {
      await setDocumentCollectionTask(task);
    }

    var latestTask = await getDocumentCollectionTask();
    if (!latestTask || latestTask.id !== task.id || latestTask.status === 'cancelled') {
      return { success: true, ignored: true };
    }
    task.status = latestTask.status;

    if (page.navigationOnly) {
      page.status = 'skipped';
      page.error = '';
      task.skipped++;
      await setDocumentCollectionTask(task);
      await moveToNextCollectionPage(task);
      return { success: true, skipped: true, navigationOnly: true };
    }

    var history = await getCollectionHistory();
    var historyKey = task.kbId + '|' + page.url;
    if (history[historyKey]) {
      page.status = 'skipped';
      page.knowledgeId = history[historyKey].knowledgeId || '';
      task.skipped++;
      await setDocumentCollectionTask(task);
      await moveToNextCollectionPage(task);
      return { success: true, skipped: true };
    }

    var extraction = await readCollectionExtraction(tabId, loadedUrl);
    var latest = await getDocumentCollectionTask();
    if (!latest || latest.id !== task.id || latest.status === 'cancelled') {
      return { success: true, ignored: true };
    }
    task.status = latest.status;
    var knowledgeId = await saveCollectionPage(task, page, extraction);
    latest = await getDocumentCollectionTask();
    if (!latest || latest.id !== task.id || latest.status === 'cancelled') {
      return { success: true, ignored: true };
    }
    task.status = latest.status;
    page.status = 'completed';
    page.title = extraction.title || page.title;
    page.resolvedUrl = extraction.url;
    page.knowledgeId = knowledgeId;
    page.error = '';
    task.completed++;
    await setDocumentCollectionTask(task);
    await moveToNextCollectionPage(task);
    return { success: true, data: page };
  } catch (error) {
    page.error = error.message || '采集失败';
    task.processing = false;
    task.processingStartedAt = 0;
    if (/(需要登录|验证码|无权访问|没有权限)/.test(page.error)) {
      page.status = 'pending';
      task.status = 'paused';
      task.error = page.error;
      await setDocumentCollectionTask(task);
      if (task.sourceTabId) {
        chrome.tabs.sendMessage(task.sourceTabId, {
          type: 'SHOW_NOTIFICATION',
          payload: { msg: page.error, status: 'error' }
        }).catch(function () {});
      }
      return { success: false, paused: true, error: page.error };
    }
    if (page.attempts < DOCUMENT_COLLECTION_MAX_ATTEMPTS) {
      page.status = 'pending';
      await setDocumentCollectionTask(task);
      await collectionDelay(2200);
      await chrome.tabs.reload(tabId);
      return { success: false, retrying: true, error: page.error };
    }
    page.status = 'failed';
    task.failed++;
    await setDocumentCollectionTask(task);
    await moveToNextCollectionPage(task);
    return { success: false, error: page.error };
  }
}

async function pauseDocumentCollection() {
  var task = await getDocumentCollectionTask();
  if (!task || task.status !== 'running') return { success: false, error: '没有正在运行的采集任务' };
  task.status = 'paused';
  task.error = '';
  await setDocumentCollectionTask(task);
  return { success: true, data: task };
}

async function resumeDocumentCollection() {
  var task = await getDocumentCollectionTask();
  if (!task || (task.status !== 'paused' && task.status !== 'running')) return { success: true, ignored: true };
  if (task.processing && Date.now() - (task.processingStartedAt || 0) < 120000) {
    return { success: true, data: task };
  }
  task.status = 'running';
  task.processing = false;
  task.processingStartedAt = 0;
  var page = task.pages[task.currentIndex];
  if (!page) {
    await finishDocumentCollection(task);
    return { success: true, data: task };
  }
  if (page.status === 'processing') page.status = 'pending';
  await setDocumentCollectionTask(task);
  await chrome.alarms.create(DOCUMENT_COLLECTION_ALARM, { delayInMinutes: 1, periodInMinutes: 1 });
  try {
    var tab = task.workerTabId ? await chrome.tabs.get(task.workerTabId) : null;
    if (!tab) throw new Error('采集标签页不存在');
    await chrome.tabs.update(task.workerTabId, { url: page.url });
  } catch (error) {
    await createCollectionWorkerTab(task);
  }
  return { success: true, data: task };
}

async function cancelDocumentCollection() {
  var task = await getDocumentCollectionTask();
  if (!task || (task.status !== 'running' && task.status !== 'paused')) {
    return { success: false, error: '没有可取消的采集任务' };
  }
  task.status = 'cancelled';
  task.processing = false;
  task.processingStartedAt = 0;
  task.completedAt = Date.now();
  await setDocumentCollectionTask(task);
  await chrome.alarms.clear(DOCUMENT_COLLECTION_ALARM);
  if (task.workerTabId) {
    try { await chrome.tabs.remove(task.workerTabId); } catch (error) {}
  }
  return { success: true, data: task };
}

// === chatbot.weixin.qq.com 扫码登录 API Helper ===
// 独立于 WeKnora 后端的第二条认证链路
// 扫码登录成功后，使用固定 API 地址 + Bearer token 访问接口
var SCAN_LOGIN_API_BASE = 'https://weknora.weixin.qq.com/api/v1';

// 扫码登录链路的 API 请求（固定地址 + Bearer token）
async function scanLoginApiRequest(method, path, body, options) {
  var data = await chrome.storage.local.get('ka_chatbot_token');
  var token = data.ka_chatbot_token;
  if (!token) {
    return { success: false, error: '未登录知识管理助手，请先扫码登录' };
  }
  var url = SCAN_LOGIN_API_BASE + path;
  var headers = {
    'Content-Type': 'application/json',
    'Authorization': 'Bearer ' + token
  };
  try {
    var fetchOpts = { method: method, headers: headers };
    if (body && method !== 'GET') {
      fetchOpts.body = JSON.stringify(body);
    }
    if (options && options.signal) {
      fetchOpts.signal = options.signal;
    }
    var result = await JiwaiNetwork.requestTextWithTimeout(fetch, url, fetchOpts);
    var resp = result.response;
    var responseText = result.text;
    if (!resp.ok) {
      var errText = resp.statusText;
      try { var errJson = JSON.parse(responseText); errText = errJson.error?.message || errJson.message || errText; } catch (e) {}
      if (resp.status === 401) {
        stopTokenKeepalive();
        broadcastTokenExpired();
        return { success: false, error: '登录已过期，请重新扫码登录', expired: true };
      }
      return JiwaiNetwork.httpFailure(resp.status, errText);
    }
    try { return JSON.parse(responseText); } catch (parseError) { return JiwaiNetwork.invalidResponseFailure(); }
  } catch (err) {
    return JiwaiNetwork.fetchFailure(err);
  }
}

// 根据当前登录方式自动选择 API 链路
// 扫码登录 → scanLoginApiRequest（固定地址 + Bearer token）
// API Key 登录 → apiRequest（配置的服务地址 + X-API-Key）
async function autoApiRequest(method, path, body, options) {
  return apiRequest(method, path, body, options);
}

// 扫码登录链路的 SSE 流式请求
async function scanLoginApiChatStream(path, body) {
  var data = await chrome.storage.local.get('ka_chatbot_token');
  var token = data.ka_chatbot_token;
  if (!token) {
    return { success: false, error: '未登录知识管理助手，请先扫码登录' };
  }
  var url = SCAN_LOGIN_API_BASE + path;
  var headers = {
    'Content-Type': 'application/json',
    'Authorization': 'Bearer ' + token,
    'Accept': 'text/event-stream'
  };
  try {
    var resp = await JiwaiNetwork.requestWithTimeout(fetch, url, {
      method: 'POST',
      headers: headers,
      body: JSON.stringify(body),
      cache: 'no-store'
    });
    if (!resp.ok) {
      var errText = resp.statusText;
      try {
        var responseText = await JiwaiNetwork.readResponseTextWithTimeout(resp);
        var errJson = JSON.parse(responseText);
        errText = errJson.error?.message || errJson.message || errText;
      } catch (e) {
        if (e && e.code === 'REQUEST_TIMEOUT') return JiwaiNetwork.fetchFailure(e);
      }
      return JiwaiNetwork.httpFailure(resp.status, errText);
    }
    return { success: true, response: resp };
  } catch (err) {
    return JiwaiNetwork.fetchFailure(err);
  }
}

// 自动选择 SSE 流式链路
async function autoApiChatStream(path, body) {
  return apiChatStream(path, body);
}

// === Token 保活 & 过期检测 ===
var TOKEN_KEEPALIVE_ALARM = 'ka-token-keepalive';
var TOKEN_KEEPALIVE_INTERVAL_MIN = 10; // 每 10 分钟 ping 一次

// 启动 token 保活定时器
async function startTokenKeepalive() {
  stopTokenKeepalive();
  await chrome.storage.local.remove('ka_chatbot_token');
}

function stopTokenKeepalive() {
  chrome.alarms.clear(TOKEN_KEEPALIVE_ALARM);
}

// 检查 token 是否仍然有效，失败则广播过期事件
async function checkTokenHealth() {
  var authData = await chrome.storage.local.get(['ka_auth', 'ka_chatbot_token']);
  if (!authData.ka_chatbot_token || !authData.ka_auth || authData.ka_auth.login_type !== 'scan') {
    stopTokenKeepalive();
    return;
  }
  var result = await scanLoginApiRequest('GET', '/auth/me');
  if (result && result.expired) {
    stopTokenKeepalive();
    broadcastTokenExpired();
  }
}

// 广播 token 过期事件到所有页面（sidepanel + popup + content scripts）
function broadcastTokenExpired() {
  chrome.runtime.sendMessage({ type: 'TOKEN_EXPIRED' }).catch(function () {});
  chrome.tabs.query({}, function (tabs) {
    tabs.forEach(function (tab) {
      if (tab.id) {
        chrome.tabs.sendMessage(tab.id, { type: 'TOKEN_EXPIRED' }).catch(function () {});
      }
    });
  });
}

chrome.alarms.onAlarm.addListener(function (alarm) {
  if (alarm.name === TOKEN_KEEPALIVE_ALARM) {
    stopTokenKeepalive();
  }
  if (alarm.name === DOCUMENT_COLLECTION_ALARM) {
    getDocumentCollectionTask().then(function (task) {
      if (task && task.status === 'running') resumeDocumentCollection();
    }).catch(function () {});
  }
});

// SW 启动时检查是否需要保活
startTokenKeepalive();
getDocumentCollectionTask().then(function (task) {
  if (task && task.status === 'running') resumeDocumentCollection();
}).catch(function () {});

chrome.tabs.onRemoved.addListener(function (tabId) {
  getDocumentCollectionTask().then(function (task) {
    if (!task || task.workerTabId !== tabId || task.status !== 'running') return;
    task.status = 'paused';
    task.workerTabId = null;
    task.processing = false;
    task.processingStartedAt = 0;
    task.error = '采集标签页已关闭，任务已暂停';
    return setDocumentCollectionTask(task);
  }).catch(function () {});
});

// === 右键菜单 ===
// 防止并发注册
var _menuSetupInProgress = false;

function setupContextMenus() {
  if (_menuSetupInProgress) return;
  _menuSetupInProgress = true;

  chrome.contextMenus.removeAll(function () {
    void chrome.runtime.lastError; // 清除可能的 lastError

    // 保存选中文字
    chrome.contextMenus.create({
      id: 'ka-save-selection',
      title: '保存到见外知识库',
      contexts: ['selection']
    }, function () { void chrome.runtime.lastError; });

    // 用选中文字提问
    chrome.contextMenus.create({
      id: 'ka-ask-selection',
      title: '使用见外知识库提问',
      contexts: ['selection']
    }, function () { void chrome.runtime.lastError; });

    // 保存图片到知识管理助手
    chrome.contextMenus.create({
      id: 'ka-save-image',
      title: '保存图片到见外知识库',
      contexts: ['image']
    }, function () { void chrome.runtime.lastError; });

    _menuSetupInProgress = false;

    // 根据登录状态更新菜单文案
    updateContextMenuTitle();
  });
}

// 插件安装/更新时注册
chrome.runtime.onInstalled.addListener(function () {
  setupContextMenus();
  // 允许 content script 访问 session storage（用于气泡禁用状态等）
  try {
    chrome.storage.session.setAccessLevel({ accessLevel: 'TRUSTED_AND_UNTRUSTED_CONTEXTS' });
  } catch (e) {}
});

// Service Worker 每次启动时也注册（确保重载后菜单存在）
setupContextMenus();

// 每次 SW 启动时也确保 session storage 访问级别正确
try {
  chrome.storage.session.setAccessLevel({ accessLevel: 'TRUSTED_AND_UNTRUSTED_CONTEXTS' });
} catch (e) {}

// 根据登录类型动态更新右键菜单中"提问"的文案
async function updateContextMenuTitle() {
  var data = await chrome.storage.local.get('ka_auth');
  var auth = data.ka_auth;
  var askTitle = '使用见外知识库提问';
  if (auth && auth.type === 'wk') {
    askTitle = '使用见外知识库提问';
  } else if (auth && auth.type === 'ka') {
    askTitle = '使用见外知识库提问';
  }
  chrome.contextMenus.update('ka-ask-selection', { title: askTitle }, function () {
    void chrome.runtime.lastError; // 菜单还未创建时静默忽略
  });
}

chrome.contextMenus.onClicked.addListener(function (info, tab) {
  // 处理图片保存（不需要 selectionText）
  if (info.menuItemId === 'ka-save-image') {
    var imgUrl = info.srcUrl;
    if (!imgUrl) return;
    var title = '图片收藏 - ' + (tab.title || '未知页面');
    var clip = {
      type: 'image-clip',
      content: '![图片](' + imgUrl + ')',
      title: title,
      meta: { url: tab.url || '', title: tab.title || '', imageUrl: imgUrl }
    };
    saveClip(clip).then(function (result) {
      if (tab && tab.id) {
        var notifMsg = '图片已保存到见外知识库';
        if (result && result.syncedToKb && result.kbName) {
          notifMsg = '图片已保存，并同步到「' + result.kbName + '」';
        }
        chrome.tabs.sendMessage(tab.id, {
          type: 'SHOW_NOTIFICATION',
          payload: { msg: notifMsg, status: 'success' }
        }).catch(function () {});
      }
    });
    return;
  }

  if (!info.selectionText) return;

  if (info.menuItemId === 'ka-save-selection') {
    // 发送到 content.js，打开 Markdown 编辑弹窗
    if (tab && tab.id) {
      chrome.tabs.sendMessage(tab.id, {
        type: 'OPEN_EDITOR_FOR_SELECTION',
        payload: { text: info.selectionText }
      }).catch(function () {});
    }
  }

  if (info.menuItemId === 'ka-ask-selection') {
    // 打开侧边栏并将选中文字作为问题
    if (tab && tab.id) {
      chrome.storage.local.set({
        ka_pending_query: { query: info.selectionText, ts: Date.now() }
      });
      chrome.sidePanel.open({ tabId: tab.id }).catch(function () {});
    }
  }
});

chrome.runtime.onMessage.addListener(function (msg, sender, sendResponse) {
  handleMessage(msg, sender).then(function (result) {
    sendResponse(result);
  }).catch(function (err) {
    sendResponse({ success: false, error: err.message || '未知错误' });
  });
  return true;
});

async function handleMessage(msg, sender) {
  switch (msg.type) {
    case 'GET_AUTH':
      return getAuth();
    case 'SET_AUTH':
      return setAuth(msg.payload);
    case 'CLEAR_AUTH':
      return clearAuth();
    case 'GET_CONFIG':
      return getConfig();
    case 'SET_CONFIG':
      return setConfig(msg.payload);
    case 'SAVE_NOTE':
      return saveNote(msg.payload);
    case 'GET_NOTES':
      return getNotes();
    case 'SAVE_CLIP':
      return saveClip(msg.payload);
    case 'GET_CLIPS':
      return getClips();
    case 'DELETE_CLIP':
      return deleteClip(msg.payload);
    case 'DELETE_NOTE':
      return deleteNote(msg.payload);
    case 'UPDATE_CLIP':
      return updateClip(msg.payload);
    case 'UPDATE_NOTE':
      return updateNote(msg.payload);
    case 'INJECT_SCRIPT':
      return injectScript(msg.payload.tabId);
    case 'ASK_WEKNORA':
      // 打开侧边栏并传递选中的文字作为问题
      if (sender && sender.tab && sender.tab.id) {
        await chrome.sidePanel.open({ tabId: sender.tab.id });
        // 存储待处理的问题，sidepanel 加载后会读取
        await chrome.storage.local.set({
          ka_pending_query: { query: msg.payload.text, ts: Date.now() }
        });
      }
      return { success: true };
    case 'SAVE_SELECTION':
      // 从气泡/编辑弹窗保存选中文字
      return saveClip(msg.payload);
    case 'OPEN_EDITOR_FOR_SELECTION':
      // 该消息通过 chrome.tabs.sendMessage 发给 content script，
      // 不应到达此处，但为安全起见兼容处理
      return { success: true };
    case 'SAVE_IMAGE':
      // 从气泡保存图片
      return saveClip(msg.payload);
    case 'CAPTURE_SCREENSHOT':
      // 截取当前标签页可见区域
      try {
        var tabId = sender && sender.tab && sender.tab.id;
        if (!tabId) return { success: false, error: '无法获取标签页' };
        var dataUrl = await chrome.tabs.captureVisibleTab(sender.tab.windowId, { format: 'jpeg', quality: 90 });
        return { success: true, dataUrl: dataUrl };
      } catch (err) {
        return { success: false, error: err.message || '截图失败' };
      }

    case 'EXTRACT_ALL_FRAMES':
      if (!sender || !sender.tab || !sender.tab.id) {
        return { success: false, error: '无法获取当前标签页' };
      }
      return extractAllFrames(sender.tab.id);

    case 'EXTRACT_FRAME_CHUNK':
      if (!sender || !sender.tab || !sender.tab.id) {
        return { success: false, error: '无法获取当前标签页' };
      }
      return readFrameExtractionChunk(
        sender.tab.id,
        msg.payload && msg.payload.frameId,
        msg.payload && msg.payload.offset,
        msg.payload && msg.payload.size
      );

    case 'CLEAR_FRAME_EXTRACTION':
      if (!sender || !sender.tab || !sender.tab.id) return { success: true };
      return clearFrameExtraction(sender.tab.id, msg.payload && msg.payload.frameId);

    case 'GET_DOCUMENT_COLLECTION_TASK':
      return { success: true, data: await getDocumentCollectionTask() };

    case 'DISCOVER_DOCUMENT_COLLECTION': {
      var discoveryTabId = msg.payload && msg.payload.tabId || sender && sender.tab && sender.tab.id;
      if (!discoveryTabId) return { success: false, error: '无法获取当前标签页' };
      var discovery = await discoverCollectionPagesInTab(discoveryTabId, msg.payload && msg.payload.maxPages);
      if (discovery.error) return { success: false, error: discovery.error };
      return { success: true, data: discovery };
    }

    case 'START_DOCUMENT_COLLECTION':
      return startDocumentCollection(msg.payload, sender);

    case 'PAUSE_DOCUMENT_COLLECTION':
      return pauseDocumentCollection();

    case 'RESUME_DOCUMENT_COLLECTION':
      return resumeDocumentCollection();

    case 'CANCEL_DOCUMENT_COLLECTION':
      return cancelDocumentCollection();

    case 'DOCUMENT_COLLECTION_PAGE_READY':
      return processDocumentCollectionPage(sender, msg.payload || {});

    // === WeKnora API 相关 ===
    case 'VALIDATE_CONFIG':
      // 仅读取当前身份，避免为一次鉴权加载完整知识库列表。
      return apiRequest('GET', '/auth/me');

    case 'LIST_KNOWLEDGE_BASES':
      // agent_id 参数仅用于共享智能体（跨租户），本地/内置智能体不传
      var agentFilter = (msg.payload && msg.payload.sharedAgentId) ? '?agent_id=' + msg.payload.sharedAgentId : '';
      return autoApiRequest('GET', '/knowledge-bases' + agentFilter);

    case 'LIST_KB_ITEMS': {
      // 拉取指定知识库的条目列表
      var kbPayload = msg.payload || {};
      var kbId = kbPayload.kbId;
      if (!kbId) return { success: false, error: '缺少知识库 ID' };
      var page = kbPayload.page || 1;
      var pageSize = kbPayload.pageSize || 5;
      return autoApiRequest('GET', '/knowledge-bases/' + kbId + '/knowledge?page=' + page + '&page_size=' + pageSize);
    }

    case 'LIST_AGENTS':
      return autoApiRequest('GET', '/agents');

    case 'GET_SUGGESTED_QUESTIONS': {
      var sqPayload = msg.payload || {};
      var sqAgentId = sqPayload.agentId;
      if (!sqAgentId) return { success: false, error: '缺少 agentId' };
      var sqQuery = 'limit=' + (sqPayload.limit || 6);
      if (sqPayload.knowledgeBaseIds && sqPayload.knowledgeBaseIds.length > 0) {
        sqQuery += '&knowledge_base_ids=' + sqPayload.knowledgeBaseIds.join(',');
      }
      return autoApiRequest('GET', '/agents/' + sqAgentId + '/suggested-questions?' + sqQuery);
    }

    case 'CREATE_SESSION':
      return autoApiRequest('POST', '/sessions', msg.payload || {});

    case 'LIST_SESSIONS':
      var p = msg.payload || {};
      return autoApiRequest('GET', '/sessions?page=' + (p.page || 1) + '&page_size=' + (p.page_size || 20));

    case 'CLEAR_SESSION_MESSAGES': {
      var sid = (msg.payload || {}).sessionId;
      if (!sid) return { success: false, error: '缺少 sessionId' };
      return autoApiRequest('DELETE', '/sessions/' + sid + '/messages');
    }

    case 'CHAT_QUERY': {
      // 真正的知识库问答 — 使用 SSE 流式输出
      var payload = msg.payload || {};
      var query = payload.query;
      if (!query) return { success: false, error: '请输入问题' };

      // 获取或创建会话
      var sessionId = payload.sessionId;
      if (!sessionId) {
        var sessionResp = await autoApiRequest('POST', '/sessions', {});
        if (sessionResp && sessionResp.success && sessionResp.data) {
          sessionId = sessionResp.data.id;
        } else if (sessionResp && sessionResp.id) {
          sessionId = sessionResp.id;
        }
        if (!sessionId) {
          return { success: false, error: '创建会话失败: ' + (sessionResp.error || '未知错误') };
        }
        await chrome.storage.local.set({ ka_current_session: sessionId });
      }

      // 确定使用知识库问答还是智能体问答
      var kbIds = payload.knowledgeBaseIds || [];
      var agentId = payload.agentId || '';
      var useAgent = payload.agentEnabled || false;
      var chatPath = useAgent
        ? '/agent-chat/' + sessionId
        : '/knowledge-chat/' + sessionId;

      // 构建完整请求体，参考 CreateKnowledgeQARequest
      var chatBody = { query: query, channel: 'browser_extension' };
      if (kbIds.length > 0) {
        chatBody.knowledge_base_ids = kbIds;
      }
      if (agentId) {
        chatBody.agent_id = agentId;
      }
      if (useAgent) {
        chatBody.agent_enabled = true;
      }
      if (payload.webSearchEnabled) {
        chatBody.web_search_enabled = true;
      }
      if (payload.mentionedItems) {
        chatBody.mentioned_items = payload.mentionedItems;
      }
      if (payload.images && payload.images.length > 0) {
        chatBody.images = payload.images;
      }

      // 使用请求 ID 区分不同来源的流式推送
      var chatRequestId = payload._requestId || (Date.now().toString(36) + Math.random().toString(36).slice(2, 6));

      // SSE 流式请求
      var streamResult = await autoApiChatStream(chatPath, chatBody);
      if (!streamResult.success) {
        return { success: false, error: streamResult.error };
      }

      // 读取 SSE 流，逐块推送到前端
      try {
        var reader = streamResult.response.body.getReader();
        var decoder = new TextDecoder();
        var fullText = '';
        var buffer = '';

        while (true) {
          var readResult = await reader.read();
          if (readResult.done) break;
          buffer += decoder.decode(readResult.value, { stream: true });

          // SSE 格式: "event: message\ndata: {json}\n\n"
          // 按双换行分割完整事件块
          var eventBlocks = buffer.split('\n\n');
          buffer = eventBlocks.pop() || '';

          for (var bi = 0; bi < eventBlocks.length; bi++) {
            var block = eventBlocks[bi].trim();
            if (!block) continue;

            // 从事件块中提取 data 行
            var dataLine = '';
            var blockLines = block.split('\n');
            for (var li = 0; li < blockLines.length; li++) {
              var bline = blockLines[li];
              if (bline.startsWith('data:')) {
                dataLine = bline.substring(5).trim();
              }
            }
            if (!dataLine || dataLine === '[DONE]') continue;

            try {
              var evt = JSON.parse(dataLine);
              var responseType = evt.response_type || '';

              // 根据 response_type 处理不同事件
              if (responseType === 'answer') {
                var chunk = evt.content || '';
                if (chunk) {
                  fullText += chunk;
                  notifyStream({
                    type: 'CHAT_STREAM_CHUNK',
                    payload: { requestId: chatRequestId, sessionId: sessionId, responseType: 'answer', content: chunk, done: !!evt.done }
                  });
                }
              } else if (responseType === 'thinking') {
                notifyStream({
                  type: 'CHAT_STREAM_CHUNK',
                  payload: {
                    requestId: chatRequestId,
                    sessionId: sessionId,
                    responseType: 'thinking',
                    content: evt.content || '',
                    eventId: evt.data && evt.data.event_id,
                    toolData: evt.data || null,
                    timestamp: evt.timestamp || Date.now()
                  }
                });
              } else if (responseType === 'tool_call') {
                notifyStream({
                  type: 'CHAT_STREAM_CHUNK',
                  payload: {
                    requestId: chatRequestId,
                    sessionId: sessionId,
                    responseType: 'tool_call',
                    content: evt.content || '',
                    toolName: evt.data && evt.data.tool_name,
                    eventId: evt.data && (evt.data.event_id || evt.data.tool_call_id),
                    toolCallId: evt.data && evt.data.tool_call_id,
                    arguments: evt.data && evt.data.arguments,
                    toolData: evt.data || null,
                    displayType: evt.display_type || (evt.data && evt.data.display_type) || '',
                    timestamp: evt.timestamp || Date.now()
                  }
                });
              } else if (responseType === 'tool_result') {
                notifyStream({
                  type: 'CHAT_STREAM_CHUNK',
                  payload: {
                    requestId: chatRequestId,
                    sessionId: sessionId,
                    responseType: 'tool_result',
                    content: evt.content || '',
                    toolName: evt.data && evt.data.tool_name,
                    eventId: evt.data && (evt.data.event_id || evt.data.tool_call_id),
                    toolCallId: evt.data && evt.data.tool_call_id,
                    success: evt.data && evt.data.success,
                    arguments: evt.data && evt.data.arguments,
                    toolData: evt.data || null,
                    displayType: evt.display_type || (evt.data && evt.data.display_type) || '',
                    timestamp: evt.timestamp || Date.now()
                  }
                });
              } else if (responseType === 'references') {
                var kRefs = evt.knowledge_references;
                if (Array.isArray(kRefs) && kRefs.length > 0) {
                  notifyStream({
                    type: 'CHAT_STREAM_CHUNK',
                    payload: { requestId: chatRequestId, sessionId: sessionId, responseType: 'references', references: kRefs }
                  });
                }
              } else if (responseType === 'error') {
                notifyStream({
                  type: 'CHAT_STREAM_CHUNK',
                  payload: {
                    requestId: chatRequestId, sessionId: sessionId, responseType: 'error',
                    content: evt.content || '请求出错', done: !!evt.done,
                    toolName: evt.data && evt.data.tool_name,
                    toolCallId: evt.data && evt.data.tool_call_id
                  }
                });
              } else if (responseType === 'complete') {
                notifyStream({
                  type: 'CHAT_STREAM_CHUNK',
                  payload: { requestId: chatRequestId, sessionId: sessionId, responseType: 'complete', done: true }
                });
              }
              if (responseType === 'session_title') {
                notifyStream({
                  type: 'CHAT_STREAM_CHUNK',
                  payload: { requestId: chatRequestId, sessionId: sessionId, responseType: 'session_title', content: evt.content || '' }
                });
              }
              // agent_query 等事件静默忽略
            } catch (e) {
              // 非 JSON data 行，忽略
            }
          }
        }

        return { success: true, data: fullText || '未获取到回复内容', sessionId: sessionId, requestId: chatRequestId };
      } catch (streamErr) {
        return { success: false, error: '读取回复流失败: ' + streamErr.message };
      }
    }

    case 'SAVE_CLIP_TO_KB': {
      // 保存剪藏内容到知识库（作为手动知识条目）
      var pl = msg.payload || {};
      if (!pl.kbId || !pl.content) return { success: false, error: '缺少知识库 ID 或内容' };
      var contentWithMeta = pl.content;
      if (pl.url) {
        contentWithMeta = '> 来源: ' + pl.url + '\n\n' + pl.content;
      }
      return autoApiRequest('POST', '/knowledge-bases/' + pl.kbId + '/knowledge/manual', {
        title: pl.title || '见外知识库剪藏',
        content: contentWithMeta,
        status: 'publish',
        channel: 'browser_extension'
      });
    }

    case 'GET_KB_KNOWLEDGE': {
      var pl = msg.payload || {};
      if (!pl.kbId || !pl.knowledgeId) return { success: false, error: '缺少知识库 ID 或知识 ID' };
      return autoApiRequest('GET', '/knowledge/' + pl.knowledgeId);
    }

    case 'UPDATE_KB_KNOWLEDGE': {
      var pl = msg.payload || {};
      if (!pl.kbId || !pl.knowledgeId) return { success: false, error: '缺少知识库 ID 或知识 ID' };
      var body = {
        channel: 'browser_extension',
        status: 'publish'
      };
      if (pl.title !== undefined) body.title = pl.title;
      if (pl.content !== undefined) body.content = pl.content;

      return autoApiRequest('PUT', '/knowledge/manual/' + pl.knowledgeId, body);
    }

    case 'FETCH_FILE': {
      // 通过 background 代理带认证头请求文件（图片等），返回 data URL
      var filePath = (msg.payload || {}).filePath;
      if (!filePath) return { success: false, error: '缺少 filePath' };

      var fileUrl, fileHeaders;
      var authCheck = await chrome.storage.local.get('ka_auth');
      if (authCheck.ka_auth && authCheck.ka_auth.login_type === 'scan') {
        // 扫码登录：用固定地址 + Bearer token
        var tokenCheck = await chrome.storage.local.get('ka_chatbot_token');
        var scanToken = tokenCheck.ka_chatbot_token;
        if (!scanToken) return { success: false, error: '未登录' };
        var scanBaseUrl = SCAN_LOGIN_API_BASE.replace(/\/api\/v\d+$/, '');
        fileUrl = scanBaseUrl + '/files?file_path=' + encodeURIComponent(filePath);
        fileHeaders = { 'Authorization': 'Bearer ' + scanToken };
      } else {
        // API Key 登录：用配置的服务地址
        var cfg = await getConfigData();
        if (!cfg || !cfg.baseUrl) return { success: false, error: '未配置服务地址' };
        var fileBaseUrl = cfg.baseUrl.replace(/\/+$/, '').replace(/\/api\/v\d+$/, '');
        fileUrl = fileBaseUrl + '/files?file_path=' + encodeURIComponent(filePath);
        fileHeaders = await buildHeaders(cfg);
      }
      try {
        var fileResult = await JiwaiNetwork.requestBlobWithTimeout(fetch, fileUrl, { method: 'GET', headers: fileHeaders });
        var fileResp = fileResult.response;
        if (!fileResp.ok) return JiwaiNetwork.httpFailure(fileResp.status, fileResp.statusText);
        var blob = fileResult.blob;
        var reader2 = new FileReader();
        var dataUrl = await new Promise(function (resolve, reject) {
          reader2.onload = function () { resolve(reader2.result); };
          reader2.onerror = function () { reject(new Error('FileReader error')); };
          reader2.readAsDataURL(blob);
        });
        return { success: true, dataUrl: dataUrl };
      } catch (err) {
        return JiwaiNetwork.fetchFailure(err);
      }
    }

    case 'GET_USER_INFO':
      return autoApiRequest('GET', '/auth/me');

    // === chatbot.weixin.qq.com API（扫码登录链路） ===
    case 'AUTH_STATE_CHANGED':
      updateContextMenuTitle();
      broadcastAuthStateChanged();
      return { success: true };

    default:
      return { success: false, error: '未知消息类型' };
  }
}

// === Auth ===
async function getAuth() {
  var data = await chrome.storage.local.get('ka_auth');
  return { success: true, data: data.ka_auth || null };
}

// 广播状态变更到所有 tab 的 content script
function broadcastAuthStateChanged() {
  chrome.tabs.query({}, function (tabs) {
    tabs.forEach(function (tab) {
      if (tab.id) {
        chrome.tabs.sendMessage(tab.id, { type: 'AUTH_STATE_CHANGED' }).catch(function () {});
      }
    });
  });
}

async function setAuth(auth) {
  await chrome.storage.local.set({ ka_auth: auth });
  updateContextMenuTitle();
  broadcastAuthStateChanged();
  // 扫码登录成功后启动 token 保活
  if (auth && auth.login_type === 'scan') {
    startTokenKeepalive();
  }
  return { success: true };
}

async function clearAuth() {
  stopTokenKeepalive();
  await chrome.storage.local.remove([
    'ka_auth',
    'ka_chatbot_token',
    'ka_clips',
    'ka_notes',
    'ka_selected_agent',
    'ka_pending_query',
    'ka_open_note',
    'clipKbId',
    'clipKbName',
    'ka_sel_bubble_enabled'
  ]);
  updateContextMenuTitle();
  broadcastAuthStateChanged();
  return { success: true };
}

// === Config (WeKnora) ===
async function getConfig() {
  var data = await chrome.storage.local.get('ka_config');
  return {
    success: true,
    data: {
      baseUrl: COMPANY_API_BASE,
      apiKey: (data.ka_config && data.ka_config.apiKey) || ''
    }
  };
}

async function setConfig(config) {
  await chrome.storage.local.set({
    ka_config: {
      baseUrl: COMPANY_API_BASE,
      apiKey: (config && config.apiKey) || ''
    }
  });
  return { success: true };
}

// === Notes (Markdown) ===
async function saveNote(note) {
  var data = await chrome.storage.local.get('ka_notes');
  var notes = data.ka_notes || [];
  note.id = Date.now().toString();
  note.createdAt = new Date().toISOString();
  notes.unshift(note);
  if (notes.length > 100) notes = notes.slice(0, 100);
  await chrome.storage.local.set({ ka_notes: notes });
  return { success: true, data: note };
}

async function getNotes() {
  var data = await chrome.storage.local.get('ka_notes');
  return { success: true, data: data.ka_notes || [] };
}

// === Clips (网页截取收藏) ===
async function saveClip(clip) {
  try {
    var data = await chrome.storage.local.get('ka_clips');
    var clips = data.ka_clips || [];

    // 如果传入了已有 id，说明是编辑已有笔记，执行更新而非新增
    if (clip.id) {
      var found = false;
      for (var i = 0; i < clips.length; i++) {
        if (clips[i].id === clip.id) {
          // 保留原始创建时间和其他元数据，只更新内容相关字段
          clips[i].content = clip.content;
          if (clip.title) clips[i].title = clip.title;
          if (clip.type) clips[i].type = clip.type;
          clips[i].updatedAt = new Date().toISOString();
          clip = clips[i]; // 返回完整的更新后记录
          found = true;
          break;
        }
      }
      // 如果在 ka_clips 中没找到，再到 ka_notes 中查找并更新
      if (!found) {
        var notesData = await chrome.storage.local.get('ka_notes');
        var notes = notesData.ka_notes || [];
        for (var j = 0; j < notes.length; j++) {
          if (notes[j].id === clip.id) {
            notes[j].content = clip.content;
            if (clip.title) notes[j].title = clip.title;
            if (clip.type) notes[j].type = clip.type;
            notes[j].updatedAt = new Date().toISOString();
            clip = notes[j];
            found = true;
            await chrome.storage.local.set({ ka_notes: notes });
            break;
          }
        }
      }
      if (found) {
        await chrome.storage.local.set({ ka_clips: clips });
        // 编辑已有记录也同步到知识库
        var editSyncResult = await syncClipToKb(clip);
        return { success: true, data: clip, syncedToKb: editSyncResult.synced, kbName: editSyncResult.kbName, syncError: editSyncResult.error || '' };
      }
      // 没找到原记录，当作新建处理（fallthrough）
    }

    // 新建记录
    clip.id = Date.now().toString();
    clip.createdAt = new Date().toISOString();
    clips.unshift(clip);
    if (clips.length > 200) clips = clips.slice(0, 200);
    await chrome.storage.local.set({ ka_clips: clips });

    // 自动同步到用户选中的知识库
    var syncResult = await syncClipToKb(clip);

    return { success: true, data: clip, syncedToKb: syncResult.synced, kbName: syncResult.kbName, syncError: syncResult.error || '' };
  } catch (err) {
    // 如果保存失败（可能是截图太大），尝试去掉截图再保存
    if (clip.screenshot) {
      try {
        delete clip.screenshot;
        var data2 = await chrome.storage.local.get('ka_clips');
        var clips2 = data2.ka_clips || [];
        clips2.unshift(clip);
        if (clips2.length > 200) clips2 = clips2.slice(0, 200);
        await chrome.storage.local.set({ ka_clips: clips2 });
        // 去掉截图后也尝试同步到知识库
        var syncResult2 = await syncClipToKb(clip);
        return { success: true, data: clip, warning: '截图过大已省略，仅保存文字', syncedToKb: syncResult2.synced, kbName: syncResult2.kbName, syncError: syncResult2.error || '' };
      } catch (err2) {
        return { success: false, error: '保存失败: ' + (err2.message || '存储空间不足') };
      }
    }
    return { success: false, error: '保存失败: ' + (err.message || '未知错误') };
  }
}

// 自动同步剪藏内容到用户选中的知识库
async function syncClipToKb(clip) {
  try {
    var kbData = await chrome.storage.local.get(['clipKbId', 'clipKbName']);
    var kbId = kbData.clipKbId;
    var kbName = kbData.clipKbName || '';

    if (!kbId) {
      return { synced: false, kbName: '' };
    }

    // 构建要保存到知识库的内容（去掉截图数据，只同步文本）
    var contentForKb = clip.content || '';
    if (clip.screenshot) {
      contentForKb += '\n\n![网页截取](' + clip.screenshot + ')';
    }
    var clipTypeMarker = '';
    if (clip && clip.type) {
      clipTypeMarker = '<!-- weknora-clip-type:' + clip.type + ' -->\n';
    }
    if (clip.meta && clip.meta.url) {
      contentForKb = '> 来源: ' + clip.meta.url + '\n\n' + contentForKb;
    }
    if (clipTypeMarker) {
      contentForKb = clipTypeMarker + contentForKb;
    }

    var kbResp = await autoApiRequest('POST', '/knowledge-bases/' + kbId + '/knowledge/manual', {
      title: clip.title || '见外知识库剪藏',
      content: contentForKb,
      status: 'publish',
      channel: 'browser_extension'
    });

    if (kbResp && kbResp.success !== false && !kbResp.error) {
      var knowledgeId = (kbResp.data && kbResp.data.id) || '';
      // 将知识 ID 和知识库 ID 写回 clip 记录
      if (knowledgeId && clip.id) {
        try {
          var stored = await chrome.storage.local.get('ka_clips');
          var allClips = stored.ka_clips || [];
          for (var ci = 0; ci < allClips.length; ci++) {
            if (allClips[ci].id === clip.id) {
              allClips[ci].knowledgeId = knowledgeId;
              allClips[ci].knowledgeBaseId = kbId;
              break;
            }
          }
          await chrome.storage.local.set({ ka_clips: allClips });
        } catch (e) {}
      }
      return { synced: true, kbName: kbName, knowledgeId: knowledgeId };
    } else {
      return { synced: false, kbName: kbName, error: (kbResp && kbResp.error) || '同步失败' };
    }
  } catch (e) {
    return { synced: false, kbName: '', error: e.message };
  }
}

async function getClips() {
  var data = await chrome.storage.local.get('ka_clips');
  return { success: true, data: data.ka_clips || [] };
}

async function deleteClip(payload) {
  var data = await chrome.storage.local.get('ka_clips');
  var clips = data.ka_clips || [];
  clips = clips.filter(function (c) { return c.id !== payload.id; });
  await chrome.storage.local.set({ ka_clips: clips });
  return { success: true };
}

async function deleteNote(payload) {
  var data = await chrome.storage.local.get('ka_notes');
  var notes = data.ka_notes || [];
  notes = notes.filter(function (n) { return n.id !== payload.id; });
  await chrome.storage.local.set({ ka_notes: notes });
  return { success: true };
}

async function updateClip(payload) {
  var data = await chrome.storage.local.get('ka_clips');
  var clips = data.ka_clips || [];
  var found = false;
  for (var i = 0; i < clips.length; i++) {
    if (clips[i].id === payload.id) {
      clips[i].content = payload.content;
      if (payload.title) clips[i].title = payload.title;
      clips[i].updatedAt = new Date().toISOString();
      found = true;
      break;
    }
  }
  if (!found) return { success: false, error: '未找到对应记录' };
  await chrome.storage.local.set({ ka_clips: clips });
  return { success: true };
}

async function updateNote(payload) {
  var data = await chrome.storage.local.get('ka_notes');
  var notes = data.ka_notes || [];
  var found = false;
  for (var i = 0; i < notes.length; i++) {
    if (notes[i].id === payload.id) {
      notes[i].content = payload.content;
      if (payload.title) notes[i].title = payload.title;
      notes[i].updatedAt = new Date().toISOString();
      found = true;
      break;
    }
  }
  if (!found) return { success: false, error: '未找到对应记录' };
  await chrome.storage.local.set({ ka_notes: notes });
  return { success: true };
}

// === Inject content script ===
async function injectScript(tabId) {
  try {
    await chrome.scripting.executeScript({ target: { tabId: tabId }, files: ['defuddle.js', 'content.js'] });
    await chrome.scripting.insertCSS({ target: { tabId: tabId }, files: ['content.css'] });
    return { success: true };
  } catch (e) {
    return { success: false, error: e.message };
  }
}

// === Commands ===
chrome.commands.onCommand.addListener(async function (cmd, tab) {
  if (!tab || !tab.id) return;
  if (cmd === 'open-sidepanel') {
    await chrome.sidePanel.open({ tabId: tab.id });
  }
  if (cmd === 'quick-ask') {
    await chrome.sidePanel.open({ tabId: tab.id });
  }
  if (cmd === 'select-clip') {
    // 快捷键触发选择剪藏：检查 content script 是否已注入
    try {
      await chrome.tabs.sendMessage(tab.id, { type: 'SELECT_CLIP' }, { frameId: 0 });
    } catch (e) {
      // content script 未注入（初次安装后未刷新页面），提示用户刷新
      chrome.tabs.sendMessage(tab.id, {
        type: 'SHOW_NOTIFICATION',
        payload: { msg: '请先刷新当前页面，再使用剪藏功能', status: 'error' }
      }).catch(function () {
        // 连通知都发不出去（页面完全没有 content script），尝试注入一个最小提示
        try {
          chrome.scripting.executeScript({
            target: { tabId: tab.id },
            func: function () { alert('插件提示：请刷新当前页面后再使用剪藏功能'); }
          });
        } catch (ignore) {}
      });
    }
  }
  if (cmd === 'quick-note') {
    // 快捷键触发快速笔记
    try {
      await chrome.tabs.sendMessage(tab.id, { type: 'QUICK_NOTE' }, { frameId: 0 });
    } catch (e) {
      chrome.tabs.sendMessage(tab.id, {
        type: 'SHOW_NOTIFICATION',
        payload: { msg: '请先刷新当前页面，再使用此功能', status: 'error' }
      }).catch(function () {
        try {
          chrome.scripting.executeScript({
            target: { tabId: tab.id },
            func: function () { alert('插件提示：请刷新当前页面后再使用此功能'); }
          });
        } catch (ignore) {}
      });
    }
  }
  if (cmd === 'smart-clip') {
    // 快捷键触发智能剪藏
    try {
      await chrome.tabs.sendMessage(tab.id, { type: 'SMART_CLIP' }, { frameId: 0 });
    } catch (e) {
      chrome.tabs.sendMessage(tab.id, {
        type: 'SHOW_NOTIFICATION',
        payload: { msg: '请先刷新当前页面，再使用剪藏功能', status: 'error' }
      }).catch(function () {
        try {
          chrome.scripting.executeScript({
            target: { tabId: tab.id },
            func: function () { alert('插件提示：请刷新当前页面后再使用剪藏功能'); }
          });
        } catch (ignore) {}
      });
    }
  }
});
