(function (global) {
  'use strict';

  var DEFAULT_MAX_PAGES = 50;
  var TRACKING_PARAMS = /^(utm_|spm$|from$|source$|timestamp$|_t$|token$|session$|sessionid$|enter_method$|is_new_|encrypt_|sign$|signature$)/i;
  var EXCLUDED_PATHS = /\/(login|logout|signin|signup|register|search|edit|create|delete|remove|settings?|account|profile|admin)(\/|$)/i;
  var EXCLUDED_FILES = /\.(?:png|jpe?g|gif|webp|svg|ico|css|js|json|xml|zip|rar|7z|pdf|docx?|xlsx?|pptx?)(?:$|\?)/i;
  var DOCUMENT_FAMILIES = [
    /\/support\/content\//i,
    /\/(?:docx|wiki)\//i,
    /\/(?:docs?|documentation|reference|api|guide|handbook|help|articles?)\//i,
    /\/doudian\/web\/article\//i
  ];
  var STRONG_CONTAINER_SELECTORS = [
    'aside', '[role="tree"]', '[role="treegrid"]', '[aria-label*="目录"]', '[aria-label*="文档"]',
    '[aria-label*="navigation" i]', '[class*="sidebar" i]', '[class*="side-bar" i]',
    '[class*="catalog" i]', '[class*="directory" i]', '[class*="doc-tree" i]',
    '[class*="menu-tree" i]', '[class*="toc" i]', '[class*="outline" i]'
  ];
  var FALLBACK_CONTAINER_SELECTORS = ['nav', '[role="navigation"]', '[class*="menu" i]'];
  var LINK_SELECTORS = ['a[href]', '[data-href]', '[data-url]', '[data-path]', '[data-route]'];
  var EXPAND_SELECTORS = [
    '[aria-expanded="false"]', 'details:not([open]) > summary',
    'button[class*="expand" i]', 'button[class*="toggle" i]',
    '[role="button"][class*="caret" i]', '[role="button"][class*="switch" i]',
    '[class*="tree-switcher"][class*="close"]', 'button.menu__caret'
  ];

  function delay(milliseconds) {
    return new Promise(function (resolve) { global.setTimeout(resolve, milliseconds); });
  }

  function normalizeText(value) {
    return String(value || '')
      .replace(/[\u200b\u200c\u200d\ufeff]/g, '')
      .replace(/\s+/g, ' ')
      .trim();
  }

  function canonicalizeUrl(value, baseUrl) {
    try {
      var parsed = new URL(value, baseUrl);
      if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') return '';
      parsed.hash = '';
      Array.from(parsed.searchParams.keys()).forEach(function (key) {
        if (TRACKING_PARAMS.test(key)) parsed.searchParams.delete(key);
      });
      parsed.pathname = parsed.pathname.replace(/\/{2,}/g, '/').replace(/\/$/, '') || '/';
      return parsed.href;
    } catch (error) {
      return '';
    }
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
    }

    visit(doc);
    return roots;
  }

  function queryAcrossRoots(roots, selectors) {
    var result = [];
    var seen = new Set();
    roots.forEach(function (root) {
      selectors.forEach(function (selector) {
        try {
          var nodes = root.querySelectorAll(selector);
          for (var i = 0; i < nodes.length; i++) {
            if (!seen.has(nodes[i])) {
              seen.add(nodes[i]);
              result.push(nodes[i]);
            }
          }
        } catch (error) {
          // 单个站点选择器失效时继续处理其它候选。
        }
      });
    });
    return result;
  }

  function getDocumentFamily(pathname) {
    for (var i = 0; i < DOCUMENT_FAMILIES.length; i++) {
      var match = String(pathname || '').match(DOCUMENT_FAMILIES[i]);
      if (match) return match[0].toLowerCase();
    }
    return '';
  }

  function getFirstPathSegment(pathname) {
    var parts = String(pathname || '').split('/').filter(Boolean);
    return parts.length ? parts[0].toLowerCase() : '';
  }

  function isNavigationOnlyUrl(value) {
    try {
      var parsed = new URL(value);
      if (parsed.pathname === '/' || /^\/category\//i.test(parsed.pathname)) return true;
      if (/(^|\.)lifexue\.com$/i.test(parsed.hostname)) {
        return /^\/(?:catalog\/|course\/?$|knowledge\/?$|case\/?$|rule\/?$)/i.test(parsed.pathname);
      }
    } catch (error) {}
    return false;
  }

  function isLikelyDocumentUrl(candidateUrl, currentUrl, strongContainer) {
    var candidate;
    var current;
    try {
      candidate = new URL(candidateUrl);
      current = new URL(currentUrl);
    } catch (error) {
      return false;
    }
    if (candidate.origin !== current.origin) return false;
    if (EXCLUDED_PATHS.test(candidate.pathname) || EXCLUDED_FILES.test(candidate.href)) return false;
    if (candidate.pathname === '/' && current.pathname !== '/') return false;
    if (/(^|\.)lifexue\.com$/i.test(current.hostname)) {
      return /^\/(?:course(?:\/detail\/[^/]+)?|catalog\/[^/]+|knowledge(?:\/|$)|case(?:\/|$)|rule(?:\/|$)|news\/detail\/[^/]+)/i.test(candidate.pathname);
    }

    var currentFamily = getDocumentFamily(current.pathname);
    var candidateFamily = getDocumentFamily(candidate.pathname);
    if (currentFamily) {
      if (currentFamily === '/docx/' || currentFamily === '/wiki/') {
        return candidateFamily === '/docx/' || candidateFamily === '/wiki/';
      }
      return candidateFamily === currentFamily;
    }
    if (candidateFamily) return true;
    if (!strongContainer) {
      var currentSegment = getFirstPathSegment(current.pathname);
      var candidateSegment = getFirstPathSegment(candidate.pathname);
      if (!currentSegment || currentSegment !== candidateSegment) return false;
    }
    return candidate.pathname !== current.pathname || candidate.search !== current.search;
  }

  function readCandidateUrl(element, baseUrl) {
    var values = [
      element.getAttribute && element.getAttribute('href'),
      element.getAttribute && element.getAttribute('data-href'),
      element.getAttribute && element.getAttribute('data-url'),
      element.getAttribute && element.getAttribute('data-path'),
      element.getAttribute && element.getAttribute('data-route')
    ];
    for (var i = 0; i < values.length; i++) {
      var value = normalizeText(values[i]);
      if (!value || value === '#' || /^javascript:/i.test(value)) continue;
      var normalized = canonicalizeUrl(value, baseUrl);
      if (normalized) return normalized;
    }
    return '';
  }

  function readFrameworkTreeRecord(element) {
    var candidates = [];
    var propertyNames = [];
    try { propertyNames = Object.getOwnPropertyNames(element); } catch (error) {}
    for (var pi = 0; pi < propertyNames.length; pi++) {
      var propertyName = propertyNames[pi];
      if (propertyName.indexOf('__reactFiber$') !== 0) continue;
      var fiber = element[propertyName];
      for (var level = 0; fiber && level < 8; level++, fiber = fiber.return) {
        var props = fiber.memoizedProps || fiber.pendingProps;
        if (!props || typeof props !== 'object') continue;
        candidates.push(props.data && props.data.data, props.data, props.node && props.node.data, props.item);
      }
    }
    try {
      var vue = element.__vueParentComponent;
      if (vue && vue.props) candidates.push(vue.props.data, vue.props.node, vue.props.item);
    } catch (error) {}

    for (var i = 0; i < candidates.length; i++) {
      var candidate = candidates[i];
      if (!candidate || typeof candidate !== 'object') continue;
      if (candidate.mappingId || candidate.url || candidate.href || candidate.path || candidate.route) return candidate;
    }
    return null;
  }

  function collectFrameworkLinks(container, currentUrl, output, seen) {
    var current;
    try { current = new URL(currentUrl); } catch (error) { return; }
    var nodes = [];
    try { nodes = Array.from(container.querySelectorAll('.base-tree-file, [class*="tree-file"]')); } catch (error) {}
    nodes.forEach(function (element) {
      var record = readFrameworkTreeRecord(element);
      if (!record) return;
      var value = record.url || record.href || record.path || record.route || '';
      if (!value && record.mappingId && /(^|\.)support\.oceanengine\.com$/i.test(current.hostname)) {
        var mapped = new URL(current.href);
        mapped.pathname = mapped.pathname.replace(/\/support\/content\/[^/]+/i, '/support/content/' + record.mappingId);
        if (record.mappingType !== undefined && record.mappingType !== null) {
          mapped.searchParams.set('mappingType', String(record.mappingType));
        }
        value = mapped.href;
      }
      var url = canonicalizeUrl(value, currentUrl);
      if (!url || seen.has(url) || !isLikelyDocumentUrl(url, currentUrl, true)) return;
      var title = normalizeText(record.mappingName || record.title || record.label || record.name || element.textContent) || '未命名文档';
      seen.add(url);
      output.push({ url: url, title: title.slice(0, 240), navigationOnly: isNavigationOnlyUrl(url) });
    });
  }

  function collectLinksFromContainer(container, currentUrl, strongContainer, output, seen) {
    var links = [];
    LINK_SELECTORS.forEach(function (selector) {
      try { links = links.concat(Array.from(container.querySelectorAll(selector))); } catch (error) {}
    });
    if (container.matches && LINK_SELECTORS.some(function (selector) {
      try { return container.matches(selector); } catch (error) { return false; }
    })) links.unshift(container);

    links.forEach(function (element) {
      var url = readCandidateUrl(element, currentUrl);
      if (!url || seen.has(url) || !isLikelyDocumentUrl(url, currentUrl, strongContainer)) return;
      var title = normalizeText(
        element.getAttribute && (element.getAttribute('aria-label') || element.getAttribute('title')) ||
        element.innerText || element.textContent
      );
      if (!title) {
        try { title = decodeURIComponent(new URL(url).pathname.split('/').filter(Boolean).pop() || '未命名文档'); } catch (error) {}
      }
      seen.add(url);
      output.push({
        url: url,
        title: title.slice(0, 240) || '未命名文档',
        navigationOnly: isNavigationOnlyUrl(url)
      });
    });
    collectFrameworkLinks(container, currentUrl, output, seen);
  }

  async function expandContainers(containers) {
    var expanded = 0;
    for (var ci = 0; ci < containers.length && expanded < 80; ci++) {
      var controls = [];
      EXPAND_SELECTORS.forEach(function (selector) {
        try { controls = controls.concat(Array.from(containers[ci].querySelectorAll(selector))); } catch (error) {}
      });
      for (var i = 0; i < controls.length && expanded < 80; i++) {
        var control = controls[i];
        var label = normalizeText(control.getAttribute('aria-label') || control.getAttribute('title') || control.textContent);
        if (/删除|退出|注销|提交|保存|delete|logout|submit|save/i.test(label)) continue;
        try {
          if (control.tagName === 'SUMMARY' && control.parentElement) control.parentElement.open = true;
          else if (!control.closest('a[href]')) control.click();
          expanded++;
        } catch (error) {}
      }
    }
    if (expanded) await delay(650);
  }

  async function collectVirtualLinks(container, currentUrl, strongContainer, output, seen) {
    collectLinksFromContainer(container, currentUrl, strongContainer, output, seen);
    var clientHeight = Number(container.clientHeight) || 0;
    var scrollHeight = Number(container.scrollHeight) || 0;
    if (!clientHeight || scrollHeight <= clientHeight + 40) return;

    var originalTop = container.scrollTop;
    var steps = Math.min(16, Math.max(2, Math.ceil(scrollHeight / Math.max(clientHeight, 1))));
    for (var step = 1; step <= steps; step++) {
      container.scrollTop = Math.round((scrollHeight - clientHeight) * step / steps);
      try { container.dispatchEvent(new Event('scroll', { bubbles: true })); } catch (error) {}
      await delay(110);
      collectLinksFromContainer(container, currentUrl, strongContainer, output, seen);
      scrollHeight = Math.max(scrollHeight, Number(container.scrollHeight) || 0);
    }
    container.scrollTop = originalTop;
    try { container.dispatchEvent(new Event('scroll', { bubbles: true })); } catch (error) {}
  }

  async function discoverDocumentLinks(doc, currentUrl, options) {
    options = options || {};
    var maxPages = Math.max(1, Math.min(Number(options.maxPages) || DEFAULT_MAX_PAGES, 200));
    var normalizedCurrent = canonicalizeUrl(currentUrl, currentUrl);
    if (!normalizedCurrent) return { pages: [], total: 0, truncated: false, error: '当前页面地址无效' };

    var roots = collectRoots(doc);
    var containers = queryAcrossRoots(roots, STRONG_CONTAINER_SELECTORS);
    var strongContainer = containers.length > 0;
    if (!containers.length) containers = queryAcrossRoots(roots, FALLBACK_CONTAINER_SELECTORS);
    if (/(^|\.)lifexue\.com$/i.test(new URL(normalizedCurrent).hostname)) {
      var contentContainers = queryAcrossRoots(roots, [
        'main', '[class*="course" i]', '[class*="catalog" i]', '[class*="news" i]',
        '[class*="rule" i]', '[class*="knowledge" i]', '[class*="case" i]'
      ]);
      var containerSet = new Set(containers);
      contentContainers.forEach(function (container) {
        if (!containerSet.has(container)) {
          containerSet.add(container);
          containers.push(container);
        }
      });
      strongContainer = true;
    }
    if (!containers.length && doc.body) containers = [doc.body];

    await expandContainers(containers);

    var pages = [];
    var seen = new Set();
    var currentTitle = normalizeText(doc.title) || '当前文档';
    seen.add(normalizedCurrent);
    pages.push({
      url: normalizedCurrent,
      title: currentTitle.slice(0, 240),
      current: true,
      navigationOnly: isNavigationOnlyUrl(normalizedCurrent)
    });

    for (var i = 0; i < containers.length; i++) {
      await collectVirtualLinks(containers[i], normalizedCurrent, strongContainer, pages, seen);
    }

    var total = pages.length;
    return {
      title: currentTitle,
      site: new URL(normalizedCurrent).hostname,
      currentUrl: normalizedCurrent,
      scope: new URL(normalizedCurrent).origin,
      pages: pages.slice(0, maxPages),
      total: total,
      truncated: total > maxPages
    };
  }

  function createTaskId() {
    return 'collection-' + Date.now().toString(36) + '-' + Math.random().toString(36).slice(2, 8);
  }

  function simpleHash(value) {
    var input = String(value || '');
    var hash = 2166136261;
    for (var i = 0; i < input.length; i++) {
      hash ^= input.charCodeAt(i);
      hash = Math.imul(hash, 16777619);
    }
    return (hash >>> 0).toString(16).padStart(8, '0');
  }

  global.JiwaiCollection = {
    DEFAULT_MAX_PAGES: DEFAULT_MAX_PAGES,
    canonicalizeUrl: canonicalizeUrl,
    isNavigationOnlyUrl: isNavigationOnlyUrl,
    discoverDocumentLinks: discoverDocumentLinks,
    createTaskId: createTaskId,
    simpleHash: simpleHash
  };
})(typeof window !== 'undefined' ? window : globalThis);
