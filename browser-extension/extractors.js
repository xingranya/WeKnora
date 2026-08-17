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

  async function extract(options) {
    options = options || {};
    var doc = options.document || global.document;
    var url = options.url || (global.location && global.location.href) || '';
    await waitForStableContent(doc, options.maxWaitMs);

    var adapter = findAdapter(url);
    var selected = selectCandidate(doc, adapter);
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
