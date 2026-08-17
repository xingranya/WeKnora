(function (global) {
  'use strict';

  var ADAPTERS = [
    {
      id: 'feishu',
      name: '飞书文档',
      hosts: ['feishu.cn', 'larksuite.com', 'larkoffice.com'],
      selectors: [
        '.docx-editor', '.docx-page-block', '.bear-web-x-container',
        '.suite-editor-container', '[data-block-id]', '[contenteditable="true"]'
      ],
      scrollSelectors: ['.docx-scroll-container', '.scroll-container', '[data-virtualized]']
    },
    {
      id: 'douyin-rules',
      name: '抖音规则中心',
      hosts: [
        'school.jinritemai.com', 'fxg.jinritemai.com', 'rule.douyin.com',
        'open.douyin.com', 'support.oceanengine.com'
      ],
      selectors: [
        'article', '.article-content', '.detail-content', '.rule-content',
        '.content-detail', '.content-richtext', '[class*="article"][class*="content"]',
        '[class*="rule"] [class*="content"]', '#knowledge-detail'
      ],
      scrollSelectors: ['[class*="scroll"]', 'main']
    },
    {
      id: 'tencent-docs',
      name: '腾讯文档',
      hosts: ['docs.qq.com'],
      selectors: [
        '.mod-editor', '.editor-container', '.canvas-content', '.kix-page-column',
        '[contenteditable="true"]', '[role="textbox"]', '[role="document"]'
      ],
      scrollSelectors: ['.scroll-container', '.editor-scrollbar', '[class*="scroll"]']
    },
    {
      id: 'yuque',
      name: '语雀',
      hosts: ['yuque.com'],
      selectors: ['.lake-content', '.ne-viewer-body', '.ne-doc-major-editor', '[data-lake-card]'],
      scrollSelectors: ['.ne-doc-major-editor', '[class*="scroll"]']
    },
    {
      id: 'notion',
      name: 'Notion',
      hosts: ['notion.so', 'notion.site'],
      selectors: ['.notion-page-content', '[data-content-editable-leaf]', '[contenteditable="true"]'],
      scrollSelectors: ['.notion-frame', '[class*="notion-scroller"]']
    },
    {
      id: 'wps',
      name: 'WPS 文档',
      hosts: ['kdocs.cn', 'wps.cn'],
      selectors: ['.doc-editor', '.kdocs-editor', '[data-type="paragraph"]', '[contenteditable="true"]'],
      scrollSelectors: ['[class*="scroll"]', '[class*="viewport"]']
    },
    {
      id: 'dingtalk',
      name: '钉钉文档',
      hosts: ['alidocs.dingtalk.com', 'docs.dingtalk.com'],
      selectors: ['.lake-content', '[class*="editor"] [contenteditable="true"]', '[role="document"]'],
      scrollSelectors: ['[class*="scroll"]', '[class*="viewport"]']
    },
    {
      id: 'google-docs',
      name: 'Google 文档',
      hosts: ['docs.google.com'],
      selectors: [
        '.kix-appview-editor', '.kix-page-column', '.kix-lineview-text-block',
        '[role="textbox"]', '[role="document"]', '[role="grid"]'
      ],
      scrollSelectors: ['.kix-appview-editor', '.docs-scrollable-container', '[class*="scroll"]']
    },
    {
      id: 'microsoft-docs',
      name: 'Microsoft 365 文档',
      hosts: ['office.com', 'officeapps.live.com', 'sharepoint.com', 'onedrive.live.com'],
      selectors: ['.CanvasZone', '.ProseMirror', '.cke_editable', '[contenteditable="true"]', '[role="document"]'],
      scrollSelectors: ['[class*="scroll"]', '[class*="viewport"]']
    }
  ];

  var GENERIC_SELECTORS = [
    'article', '[role="article"]', 'main', '[role="main"]', '[role="document"]',
    '.post-content', '.article-content', '.entry-content', '.post-body', '.article-body',
    '[class*="rich-text"]', '[class*="richtext"]', '[contenteditable="true"]'
  ];

  var SKIP_SELECTOR = [
    'script', 'style', 'noscript', 'template', 'svg', 'nav', 'header', 'footer',
    '[role="navigation"]', '[role="banner"]', '[aria-hidden="true"]',
    '[hidden]', '.ads', '.advertisement', '.sidebar', '.comments'
  ].join(',');

  function normalizeWhitespace(value) {
    return String(value || '')
      .replace(/\u00a0/g, ' ')
      .replace(/[\u200b\u200c\u200d\ufeff]/g, '')
      .replace(/[ \t]+/g, ' ')
      .replace(/ *\n */g, '\n')
      .replace(/\n{3,}/g, '\n\n')
      .trim();
  }

  function hostMatches(hostname, suffix) {
    return hostname === suffix || hostname.endsWith('.' + suffix);
  }

  function findAdapter(url) {
    var hostname = '';
    try {
      hostname = new URL(url).hostname.toLowerCase();
    } catch (e) {
      return null;
    }
    for (var i = 0; i < ADAPTERS.length; i++) {
      if (ADAPTERS[i].hosts.some(function (host) { return hostMatches(hostname, host); })) {
        return ADAPTERS[i];
      }
    }
    return null;
  }

  function collectRoots(doc) {
    var roots = [doc];
    var visited = new Set();

    function visit(root) {
      if (!root || visited.has(root) || typeof root.querySelectorAll !== 'function') return;
      visited.add(root);
      var elements = root.querySelectorAll('*');
      for (var i = 0; i < elements.length; i++) {
        if (elements[i].shadowRoot) {
          roots.push(elements[i].shadowRoot);
          visit(elements[i].shadowRoot);
        }
      }
      var frames = root.querySelectorAll('iframe');
      for (var j = 0; j < frames.length; j++) {
        try {
          if (frames[j].contentDocument) {
            roots.push(frames[j].contentDocument);
            visit(frames[j].contentDocument);
          }
        } catch (e) {
          // 跨域 iframe 无法读取，交由页面自身可访问的无障碍 DOM 兜底。
        }
      }
    }

    visit(doc);
    return roots;
  }

  function queryAcrossRoots(roots, selectors) {
    var results = [];
    var seen = new Set();
    selectors.forEach(function (selector) {
      roots.forEach(function (root) {
        try {
          var nodes = root.querySelectorAll(selector);
          for (var i = 0; i < nodes.length; i++) {
            if (!seen.has(nodes[i])) {
              seen.add(nodes[i]);
              results.push(nodes[i]);
            }
          }
        } catch (e) {
          // 单个站点选择器失效时继续尝试其他候选。
        }
      });
    });
    return results;
  }

  function isIgnored(element) {
    if (!element || element.nodeType !== 1) return false;
    try {
      if (element.matches(SKIP_SELECTOR)) return true;
      var style = global.getComputedStyle ? global.getComputedStyle(element) : null;
      return !!(style && (style.display === 'none' || style.visibility === 'hidden'));
    } catch (e) {
      return false;
    }
  }

  function escapeMarkdown(value) {
    return normalizeWhitespace(value).replace(/([\\`*_[\]<>])/g, '\\$1');
  }

  function absoluteUrl(value, baseUrl) {
    if (!value) return '';
    if (/^(data|blob):/i.test(value)) return '';
    try {
      var parsed = new URL(value, baseUrl);
      if (hostMatches(parsed.hostname, 'feishu.cn') ||
          hostMatches(parsed.hostname, 'larksuite.com') ||
          hostMatches(parsed.hostname, 'larkoffice.com')) {
        if (/\/docx\//.test(parsed.pathname) && parsed.searchParams.has('opendoc')) {
          var editionId = parsed.searchParams.get('edition_id');
          parsed.search = '';
          if (editionId) parsed.searchParams.set('edition_id', editionId);
          parsed.hash = '';
        }
      }
      return parsed.href;
    } catch (e) {
      return value;
    }
  }

  function renderChildren(element, context) {
    var output = '';
    for (var i = 0; i < element.childNodes.length; i++) {
      output += renderNode(element.childNodes[i], context);
    }
    return output;
  }

  function renderTableRow(element, context) {
    var cells = Array.prototype.slice.call(element.querySelectorAll(':scope > th, :scope > td'));
    if (!cells.length) return '';
    var values = cells.map(function (cell) {
      return normalizeWhitespace(renderChildren(cell, context)).replace(/\|/g, '\\|');
    });
    var row = '| ' + values.join(' | ') + ' |\n';
    if (cells.some(function (cell) { return cell.tagName.toLowerCase() === 'th'; })) {
      row += '| ' + values.map(function () { return '---'; }).join(' | ') + ' |\n';
    }
    return row;
  }

  function renderNode(node, context) {
    if (!node) return '';
    if (node.nodeType === 3) return node.nodeValue || '';
    if (node.nodeType !== 1 || isIgnored(node)) return '';

    var tag = node.tagName.toLowerCase();
    var content = renderChildren(node, context);
    var normalized = normalizeWhitespace(content);
    var level;

    if (/^h[1-6]$/.test(tag)) {
      level = Number(tag.slice(1));
      return '\n\n' + new Array(level + 1).join('#') + ' ' + normalized + '\n\n';
    }
    if (tag === 'p' || tag === 'section' || tag === 'article') return '\n\n' + normalized + '\n\n';
    if (tag === 'br') return '\n';
    if (tag === 'li') {
      var ordered = node.parentElement && node.parentElement.tagName.toLowerCase() === 'ol';
      return '\n' + (ordered ? '1. ' : '- ') + normalized;
    }
    if (tag === 'blockquote') return '\n\n> ' + normalized.replace(/\n/g, '\n> ') + '\n\n';
    if (tag === 'pre') return '\n\n```\n' + String(node.textContent || '').trim() + '\n```\n\n';
    if (tag === 'tr') return renderTableRow(node, context);
    if (tag === 'a') {
      var href = absoluteUrl(node.getAttribute('href'), context.url);
      return href && normalized ? '[' + normalized + '](' + href + ')' : normalized;
    }
    if (tag === 'img') {
      var source = absoluteUrl(node.currentSrc || node.getAttribute('src') || node.getAttribute('data-src'), context.url);
      var alt = escapeMarkdown(node.getAttribute('alt') || '图片');
      return source ? '![' + alt + '](' + source + ')' : '';
    }

    if (!normalized && /^(canvas|div|span)$/.test(tag)) {
      var ariaText = node.getAttribute('aria-label') || node.getAttribute('data-label') || '';
      if (ariaText.length > 1 && ariaText.length < 2000) return '\n' + ariaText + '\n';
    }
    if (/^(div|main|aside|ul|ol|table|tbody|thead)$/.test(tag)) return '\n' + content + '\n';
    return content;
  }

  function toMarkdown(element, url) {
    if (!element) return '';
    var markdown = renderNode(element, { url: url });
    return normalizeWhitespace(markdown);
  }

  function candidateScore(element, minimumLength) {
    var text = normalizeWhitespace(element.innerText || element.textContent || '');
    if (text.length < (minimumLength || 40)) return 0;
    var linkText = '';
    try {
      linkText = Array.prototype.slice.call(element.querySelectorAll('a')).map(function (link) {
        return link.innerText || link.textContent || '';
      }).join('');
    } catch (e) {}
    var densityPenalty = Math.min(linkText.length / Math.max(text.length, 1), 0.8);
    var semanticBonus = /^(ARTICLE|MAIN)$/.test(element.tagName) || element.getAttribute('role') === 'document' ? 1.25 : 1;
    return text.length * (1 - densityPenalty) * semanticBonus;
  }

  function selectCandidate(doc, adapter) {
    var roots = collectRoots(doc);
    var candidates = adapter ? queryAcrossRoots(roots, adapter.selectors) : [];
    candidates = candidates.filter(function (candidate) { return candidateScore(candidate, 10) > 0; });
    if (!candidates.length) {
      candidates = queryAcrossRoots(roots, GENERIC_SELECTORS);
      if (doc.body) candidates.push(doc.body);
    }
    candidates.sort(function (left, right) {
      return candidateScore(right, adapter ? 10 : 40) - candidateScore(left, adapter ? 10 : 40);
    });
    return { element: candidates[0] || doc.body, roots: roots };
  }

  function findScrollContainer(element, roots, adapter) {
    var current = element;
    while (current && current !== current.ownerDocument.body) {
      if (current.scrollHeight > current.clientHeight + 120) return current;
      current = current.parentElement;
    }
    if (adapter) {
      var candidates = queryAcrossRoots(roots, adapter.scrollSelectors || []);
      for (var i = 0; i < candidates.length; i++) {
        if (candidates[i].scrollHeight > candidates[i].clientHeight + 120) return candidates[i];
      }
    }
    return null;
  }

  function mergeMarkdown(parts) {
    var seen = new Set();
    var merged = [];
    parts.forEach(function (part) {
      normalizeWhitespace(part).split(/\n{2,}/).forEach(function (block) {
        var normalized = normalizeWhitespace(block);
        if (normalized && !seen.has(normalized)) {
          seen.add(normalized);
          merged.push(normalized);
        }
      });
    });
    return merged.join('\n\n');
  }

  function delay(milliseconds) {
    return new Promise(function (resolve) { global.setTimeout(resolve, milliseconds); });
  }

  async function materializeVirtualContent(element, roots, adapter, url) {
    var snapshots = [toMarkdown(element, url)];
    var scrollContainer = findScrollContainer(element, roots, adapter);
    if (!scrollContainer) return snapshots[0];

    var originalTop = scrollContainer.scrollTop;
    var lastHeight = 0;
    var stableRounds = 0;
    for (var i = 0; i < 18; i++) {
      var maxTop = Math.max(scrollContainer.scrollHeight - scrollContainer.clientHeight, 0);
      var nextTop = Math.min(maxTop, Math.round((i + 1) * Math.max(scrollContainer.clientHeight * 0.85, 360)));
      scrollContainer.scrollTop = nextTop;
      scrollContainer.dispatchEvent(new Event('scroll', { bubbles: true }));
      await delay(180);

      var refreshed = selectCandidate(element.ownerDocument, adapter).element || element;
      snapshots.push(toMarkdown(refreshed, url));
      if (scrollContainer.scrollHeight === lastHeight && nextTop >= maxTop) stableRounds += 1;
      else stableRounds = 0;
      lastHeight = scrollContainer.scrollHeight;
      if (stableRounds >= 2) break;
    }
    scrollContainer.scrollTop = originalTop;
    scrollContainer.dispatchEvent(new Event('scroll', { bubbles: true }));
    return mergeMarkdown(snapshots);
  }

  function waitForStableContent(doc, maxWaitMs) {
    var timeout = Math.max(800, Math.min(maxWaitMs || 5000, 8000));
    return new Promise(function (resolve) {
      var finished = false;
      var quietTimer;
      var hardTimer;
      var observer;

      function finish() {
        if (finished) return;
        finished = true;
        if (quietTimer) global.clearTimeout(quietTimer);
        if (hardTimer) global.clearTimeout(hardTimer);
        if (observer) observer.disconnect();
        resolve();
      }

      function scheduleQuietFinish() {
        if (quietTimer) global.clearTimeout(quietTimer);
        quietTimer = global.setTimeout(finish, 650);
      }

      hardTimer = global.setTimeout(finish, timeout);
      if (!doc.documentElement || typeof MutationObserver === 'undefined') {
        scheduleQuietFinish();
        return;
      }
      observer = new MutationObserver(scheduleQuietFinish);
      observer.observe(doc.documentElement, { subtree: true, childList: true, characterData: true });
      global.setTimeout(scheduleQuietFinish, 250);
    });
  }

  function getMetaContent(doc, name) {
    var element = doc.querySelector('meta[name="' + name + '"], meta[property="' + name + '"]');
    return element ? (element.getAttribute('content') || '') : '';
  }

  function directSemanticBlocks(container) {
    if (!container || typeof container.querySelectorAll !== 'function') return [];
    return Array.prototype.slice.call(container.querySelectorAll('[data-block-type]')).filter(function (element) {
      var current = element.parentElement;
      while (current && current !== container) {
        if (current.hasAttribute('data-block-type')) return false;
        current = current.parentElement;
      }
      return current === container;
    });
  }

  function markdownWrap(content, marker) {
    var clean = normalizeWhitespace(content);
    return clean ? marker + clean + marker : '';
  }

  async function blobToDataURL(blob) {
    return new Promise(function (resolve, reject) {
      var reader = new FileReader();
      reader.onload = function () { resolve(String(reader.result || '')); };
      reader.onerror = reject;
      reader.readAsDataURL(blob);
    });
  }

  async function compressLargeImage(blob) {
    if (blob.size <= 400 * 1024 || typeof createImageBitmap !== 'function') return blob;
    try {
      var bitmap = await createImageBitmap(blob);
      var maxDimension = 2400;
      var ratio = Math.min(1, maxDimension / Math.max(bitmap.width, bitmap.height));
      var canvas = document.createElement('canvas');
      canvas.width = Math.max(1, Math.round(bitmap.width * ratio));
      canvas.height = Math.max(1, Math.round(bitmap.height * ratio));
      var context = canvas.getContext('2d');
      context.drawImage(bitmap, 0, 0, canvas.width, canvas.height);
      if (bitmap.close) bitmap.close();
      var compressed = await new Promise(function (resolve) {
        canvas.toBlob(resolve, 'image/webp', 0.94);
      });
      return compressed || blob;
    } catch (e) {
      return blob;
    }
  }

  async function resolveRichImage(image, context, fallbackAlt) {
    var source = image && (image.currentSrc || image.getAttribute('src') || image.getAttribute('data-src'));
    if (!source) return '';
    var alt = normalizeWhitespace((image.getAttribute('alt') || fallbackAlt || '图片').replace(/^飞书文档\s*-\s*/, '')) || '图片';
    if (context.imageCache.has(source)) {
      var cached = context.imageCache.get(source);
      return cached ? '![' + escapeMarkdown(alt) + '](' + cached + ')' : '> 图片：' + alt;
    }

    if (/^data:image\//i.test(source)) {
      if (context.totalImageChars + source.length > context.maxImageChars) {
        context.imageCache.set(source, '');
        return '> 图片因文档体积限制未内嵌：' + alt;
      }
      context.totalImageChars += source.length;
      context.imageCount += 1;
      var dataToken = '__JIWAI_IMAGE_' + context.imagePayloads.length + '__';
      context.imagePayloads.push({ token: dataToken, dataURL: source });
      context.imageCache.set(source, dataToken);
      return '![' + escapeMarkdown(alt) + '](' + dataToken + ')';
    }

    if (!/^blob:/i.test(source)) {
      var stableSource = absoluteUrl(source, context.url);
      context.imageCache.set(source, stableSource);
      return stableSource ? '![' + escapeMarkdown(alt) + '](' + stableSource + ')' : '> 图片：' + alt;
    }

    try {
      var dataURL = await Promise.race([
        (async function () {
          var response = await fetch(source);
          var blob = await response.blob();
          blob = await compressLargeImage(blob);
          return blobToDataURL(blob);
        })(),
        new Promise(function (_, reject) {
          global.setTimeout(function () { reject(new Error('图片读取超时')); }, 3500);
        })
      ]);
      if (context.totalImageChars + dataURL.length > context.maxImageChars) {
        context.imageCache.set(source, '');
        return '> 图片因文档体积限制未内嵌：' + alt;
      }
      context.totalImageChars += dataURL.length;
      context.imageCount += 1;
      var token = '__JIWAI_IMAGE_' + context.imagePayloads.length + '__';
      context.imagePayloads.push({ token: token, dataURL: dataURL });
      context.imageCache.set(source, token);
      return '![' + escapeMarkdown(alt) + '](' + token + ')';
    } catch (e) {
      context.imageCache.set(source, '');
      return '> 图片读取失败：' + alt;
    }
  }

  async function serializeRichInline(node, context) {
    if (!node) return '';
    if (node.nodeType === 3) return String(node.nodeValue || '').replace(/[\u200b\u200c\u200d\ufeff]/g, '');
    if (node.nodeType !== 1 || isIgnored(node)) return '';

    var tag = node.tagName.toLowerCase();
    if (tag === 'br') return '\n';
    if (tag === 'img') return resolveRichImage(node, context, '图片');

    var children = '';
    for (var i = 0; i < node.childNodes.length; i++) {
      children += await serializeRichInline(node.childNodes[i], context);
    }
    var clean = normalizeWhitespace(children);
    if (!clean) return '';

    if (tag === 'a') {
      var href = absoluteUrl(node.getAttribute('href'), context.url);
      return href ? '[' + clean + '](' + href + ')' : clean;
    }
    var style = String(node.getAttribute('style') || '').toLowerCase();
    if (tag === 'strong' || tag === 'b' || /font-weight\s*:\s*(bold|[6-9]00)/.test(style)) {
      return markdownWrap(clean, '**');
    }
    if (tag === 'em' || tag === 'i' || /font-style\s*:\s*italic/.test(style)) {
      return markdownWrap(clean, '*');
    }
    if (tag === 'del' || tag === 's' || /text-decoration[^;]*line-through/.test(style)) {
      return markdownWrap(clean, '~~');
    }
    if (tag === 'code') return '`' + clean.replace(/`/g, '\\`') + '`';
    if (/^(div|p|section)$/.test(tag) && node.hasAttribute('data-block-type')) return clean + '\n';
    return children;
  }

  async function serializeFeishuContainer(container, context) {
    var blocks = directSemanticBlocks(container);
    if (!blocks.length) return normalizeWhitespace(await serializeRichInline(container, context));
    var parts = [];
    for (var i = 0; i < blocks.length; i++) {
      var part = await serializeFeishuBlock(blocks[i], context);
      if (part) parts.push(part);
    }
    return parts.join('\n\n');
  }

  function tableCellMarkdown(value) {
    return normalizeWhitespace(value)
      .replace(/\|/g, '\\|')
      .replace(/\n+/g, '<br>');
  }

  async function serializeFeishuTable(block, context) {
    var table = block.querySelector('table');
    if (!table) return serializeFeishuContainer(block, context);
    var rows = Array.prototype.slice.call(table.querySelectorAll('tr')).filter(function (row) {
      return row.closest('table') === table;
    });
    var renderedRows = [];
    var maxColumns = 0;
    for (var rowIndex = 0; rowIndex < rows.length; rowIndex++) {
      var cells = Array.prototype.slice.call(rows[rowIndex].querySelectorAll(':scope > th, :scope > td'));
      if (!cells.length) continue;
      var renderedCells = [];
      for (var cellIndex = 0; cellIndex < cells.length; cellIndex++) {
        var value = await serializeFeishuContainer(cells[cellIndex], context);
        renderedCells.push(tableCellMarkdown(value));
        var colspan = Number(cells[cellIndex].getAttribute('colspan') || 1);
        for (var span = 1; span < colspan; span++) renderedCells.push('');
      }
      maxColumns = Math.max(maxColumns, renderedCells.length);
      renderedRows.push(renderedCells);
    }
    if (!renderedRows.length) return '';
    maxColumns = Math.max(maxColumns, 1);
    renderedRows.forEach(function (row) {
      while (row.length < maxColumns) row.push('');
    });
    var header = renderedRows[0].map(function (value, index) { return value || ('列 ' + (index + 1)); });
    var lines = [
      '| ' + header.join(' | ') + ' |',
      '| ' + header.map(function () { return '---'; }).join(' | ') + ' |'
    ];
    for (var i = 1; i < renderedRows.length; i++) {
      lines.push('| ' + renderedRows[i].join(' | ') + ' |');
    }
    return lines.join('\n');
  }

  async function serializeFeishuBlock(block, context) {
    var type = String(block.getAttribute('data-block-type') || '').toLowerCase();
    var blockImages = Array.prototype.slice.call(block.querySelectorAll('img'));
    if (blockImages.length) {
      await Promise.all(blockImages.map(function (image) {
        return resolveRichImage(image, context, normalizeWhitespace(block.innerText || block.textContent || '') || '图片');
      }));
    }
    if (type === 'page' || type === 'table_cell' || type === 'grid_column') {
      return serializeFeishuContainer(block, context);
    }
    if (type === 'table') return serializeFeishuTable(block, context);
    if (type === 'grid') return serializeFeishuContainer(block, context);
    if (type === 'image') {
      var image = block.querySelector('img');
      var imageAlt = normalizeWhitespace(block.innerText || block.textContent || '') || '图片';
      return image ? resolveRichImage(image, context, imageAlt) : '> 图片：' + imageAlt;
    }
    if (/^heading[1-6]$/.test(type)) {
      var level = Number(type.slice(-1));
      var heading = normalizeWhitespace(await serializeRichInline(block, context));
      return heading ? new Array(level + 1).join('#') + ' ' + heading : '';
    }
    if (type === 'callout') {
      var calloutText = normalizeWhitespace(await serializeFeishuContainer(block, context));
      var rawText = normalizeWhitespace(block.innerText || block.textContent || '');
      var iconMatch = rawText.match(/[\u2600-\u27bf\u{1f300}-\u{1faff}]/u);
      var icon = iconMatch ? iconMatch[0] + ' ' : '';
      return calloutText ? '> ' + icon + calloutText.replace(/\n/g, '\n> ') : '';
    }
    if (type === 'quote_container' || type === 'quote') {
      var quote = normalizeWhitespace(await serializeFeishuContainer(block, context));
      return quote ? '> ' + quote.replace(/\n/g, '\n> ') : '';
    }
    if (type === 'bullet' || type === 'bulleted_list') {
      var bullet = normalizeWhitespace(await serializeRichInline(block, context)).replace(/^[•◦▪]\s*/, '');
      return bullet ? '- ' + bullet.replace(/\n/g, '\n  ') : '';
    }
    if (type === 'ordered' || type === 'numbered_list') {
      var ordered = normalizeWhitespace(await serializeRichInline(block, context)).replace(/^\d+[.)、]?\s*/, '');
      return ordered ? '1. ' + ordered.replace(/\n/g, '\n   ') : '';
    }
    if (type === 'todo' || type === 'task') {
      var todo = normalizeWhitespace(await serializeRichInline(block, context));
      return todo ? '- [ ] ' + todo : '';
    }
    if (type === 'code' || type === 'code_block') {
      return '```\n' + String(block.innerText || block.textContent || '').trim() + '\n```';
    }
    if (type === 'divider') return '---';
    return normalizeWhitespace(await serializeRichInline(block, context));
  }

  async function extractFeishuDocument(doc, url) {
    var scrollContainer = doc.querySelector('.bear-web-x-container');
    var originalTop = scrollContainer ? scrollContainer.scrollTop : 0;
    var originalClassName = scrollContainer ? scrollContainer.className : '';
    var originalStyle = scrollContainer ? scrollContainer.getAttribute('style') : null;
    var context = {
      url: url,
      imageCache: new Map(),
      imageCount: 0,
      totalImageChars: 0,
      maxImageChars: 24 * 1024 * 1024,
      imagePayloads: []
    };
    var records = new Map();
    global.__jiwaiExtractionStage = { stage: 'scroll', blockCount: 0, imageCount: 0 };

    function captureVisibleBlocks() {
      var pages = Array.prototype.slice.call(doc.querySelectorAll('.docx-page-block'));
      for (var pageIndex = 0; pageIndex < pages.length; pageIndex++) {
        var blocks = directSemanticBlocks(pages[pageIndex]);
        for (var blockIndex = 0; blockIndex < blocks.length; blockIndex++) {
          var block = blocks[blockIndex];
          var recordID = block.getAttribute('data-record-id') || block.getAttribute('data-block-id') || '';
          var blockID = Number(block.getAttribute('data-block-id'));
          var key = recordID || ('block-' + blockID + '-' + blockIndex);
          var existing = records.get(key);
          var htmlLength = block.innerHTML.length;
          if (!existing || htmlLength > existing.htmlLength) {
            records.set(key, {
              order: Number.isFinite(blockID) ? blockID : records.size + 100000,
              htmlLength: htmlLength,
              node: block.cloneNode(true)
            });
          }
        }
      }
    }

    if (scrollContainer) {
      scrollContainer.classList.remove('opendoc-unscrollable');
      scrollContainer.style.setProperty('overflow-y', 'auto', 'important');
      scrollContainer.style.setProperty('pointer-events', 'auto', 'important');
      scrollContainer.scrollTop = 0;
      scrollContainer.dispatchEvent(new Event('scroll', { bubbles: true }));
      await delay(180);
    }

    for (var step = 0; step < 120; step++) {
      captureVisibleBlocks();
      global.__jiwaiExtractionStage = { stage: 'scroll', blockCount: records.size, step: step };
      if (!scrollContainer) break;
      var maxTop = Math.max(scrollContainer.scrollHeight - scrollContainer.clientHeight, 0);
      if (scrollContainer.scrollTop >= maxTop - 2) {
        await delay(220);
        captureVisibleBlocks();
        break;
      }
      var nextTop = Math.min(maxTop, scrollContainer.scrollTop + Math.max(scrollContainer.clientHeight * 2, 1200));
      scrollContainer.scrollTop = nextTop;
      scrollContainer.dispatchEvent(new Event('scroll', { bubbles: true }));
      await delay(550);
    }

    if (scrollContainer) {
      scrollContainer.scrollTop = originalTop;
      scrollContainer.dispatchEvent(new Event('scroll', { bubbles: true }));
      scrollContainer.className = originalClassName;
      if (originalStyle === null) scrollContainer.removeAttribute('style');
      else scrollContainer.setAttribute('style', originalStyle);
    }

    var orderedRecords = Array.from(records.values())
      .sort(function (left, right) { return left.order - right.order; });
    var imageEntries = [];
    var seenImages = new Set();
    orderedRecords.forEach(function (record) {
      Array.prototype.slice.call(record.node.querySelectorAll('img')).forEach(function (image) {
        var source = image.getAttribute('src') || image.getAttribute('data-src') || '';
        if (!source || seenImages.has(source)) return;
        seenImages.add(source);
        imageEntries.push({ image: image, alt: normalizeWhitespace(record.node.innerText || record.node.textContent || '') || '图片' });
      });
    });

    var imageIndex = 0;
    var imageDeadline = Date.now() + 9000;
    global.__jiwaiExtractionStage = { stage: 'images', blockCount: records.size, imageTotal: imageEntries.length, imageDone: 0 };
    async function imageWorker() {
      while (imageIndex < imageEntries.length && Date.now() < imageDeadline) {
        var entry = imageEntries[imageIndex++];
        await resolveRichImage(entry.image, context, entry.alt);
        global.__jiwaiExtractionStage = {
          stage: 'images',
          blockCount: records.size,
          imageTotal: imageEntries.length,
          imageDone: context.imageCache.size
        };
      }
    }
    var workerCount = Math.min(6, imageEntries.length);
    var workers = [];
    for (var workerIndex = 0; workerIndex < workerCount; workerIndex++) workers.push(imageWorker());
    await Promise.all(workers);
    for (; imageIndex < imageEntries.length; imageIndex++) {
      var unresolvedSource = imageEntries[imageIndex].image.getAttribute('src') || '';
      if (unresolvedSource && !context.imageCache.has(unresolvedSource)) context.imageCache.set(unresolvedSource, '');
    }

    global.__jiwaiExtractionStage = { stage: 'serialize', blockCount: records.size, imageCount: context.imageCount };
    var markdownParts = [];
    for (var recordIndex = 0; recordIndex < orderedRecords.length; recordIndex++) {
      var markdownPart = await serializeFeishuBlock(orderedRecords[recordIndex].node, context);
      if (markdownPart) markdownParts.push(markdownPart);
    }
    var markdown = normalizeWhitespace(markdownParts.join('\n\n'));
    context.imagePayloads.forEach(function (payload) {
      markdown = markdown.split(payload.token).join(payload.dataURL);
    });
    global.__jiwaiExtractionStage = { stage: 'done', blockCount: records.size, imageCount: context.imageCount, length: markdown.length };
    return {
      markdown: markdown,
      imageCount: context.imageCount,
      blockCount: records.size
    };
  }

  async function extract(options) {
    options = options || {};
    var doc = options.document || global.document;
    var url = options.url || (global.location && global.location.href) || '';
    var adapter = findAdapter(url);
    await waitForStableContent(doc, adapter && adapter.id === 'feishu' ? 1200 : options.maxWaitMs);
    if (adapter && adapter.id === 'feishu' && doc.querySelector('.docx-page-block')) {
      var feishuResult = await extractFeishuDocument(doc, url);
      if (feishuResult.markdown) {
        return {
          markdown: feishuResult.markdown,
          title: getMetaContent(doc, 'og:title') || doc.title || adapter.name,
          author: getMetaContent(doc, 'author') || '',
          description: getMetaContent(doc, 'description') || '',
          site: adapter.name,
          published: '',
          matchedSite: true,
          adapterId: adapter.id,
          imageCount: feishuResult.imageCount,
          blockCount: feishuResult.blockCount
        };
      }
    }
    var selected = selectCandidate(doc, adapter);
    if (adapter && adapter.id === 'douyin-rules' && doc.querySelector('tt-docs-component')) {
      return {
        markdown: toMarkdown(selected.element, url),
        title: getMetaContent(doc, 'og:title') || doc.title || adapter.name,
        author: getMetaContent(doc, 'author') || '',
        description: getMetaContent(doc, 'description') || '',
        site: adapter.name,
        published: getMetaContent(doc, 'article:published_time'),
        matchedSite: true,
        adapterId: adapter.id
      };
    }
    var markdown = await materializeVirtualContent(selected.element, selected.roots, adapter, url);

    return {
      markdown: markdown,
      title: getMetaContent(doc, 'og:title') || doc.title || (adapter && adapter.name) || '网页采集',
      author: getMetaContent(doc, 'author') || getMetaContent(doc, 'article:author'),
      description: getMetaContent(doc, 'description') || getMetaContent(doc, 'og:description'),
      site: getMetaContent(doc, 'og:site_name') || (adapter && adapter.name) || (global.location && global.location.hostname) || '',
      published: getMetaContent(doc, 'article:published_time'),
      matchedSite: !!adapter,
      adapterId: adapter ? adapter.id : 'generic'
    };
  }

  global.JiwaiPageExtractor = {
    adapters: ADAPTERS,
    extract: extract,
    findAdapter: findAdapter,
    toMarkdown: toMarkdown,
    waitForStableContent: waitForStableContent
  };
})(typeof window !== 'undefined' ? window : globalThis);
