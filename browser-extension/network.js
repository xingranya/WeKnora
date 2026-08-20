(function (global) {
  'use strict';

  var COMPANY_API_BASE = 'https://know.seeway.co/api/v1';
  var REQUEST_TIMEOUT_MS = 12000;
  var MESSAGE_TIMEOUT_MS = 15000;

  function makeError(message, code) {
    var error = new Error(message);
    error.code = code;
    return error;
  }

  async function runWithTimeout(externalSignal, timeoutMs, operation) {
    var controller = new AbortController();
    var timedOut = false;
    var timeout = Number(timeoutMs) > 0 ? Number(timeoutMs) : REQUEST_TIMEOUT_MS;
    var onExternalAbort = function () {
      controller.abort(externalSignal && externalSignal.reason);
    };

    if (externalSignal) {
      if (externalSignal.aborted) {
        onExternalAbort();
      } else {
        externalSignal.addEventListener('abort', onExternalAbort, { once: true });
      }
    }

    var timer = setTimeout(function () {
      timedOut = true;
      controller.abort();
    }, timeout);

    try {
      return await operation(controller.signal);
    } catch (error) {
      if (timedOut) {
        throw makeError('请求超时', 'REQUEST_TIMEOUT');
      }
      throw error;
    } finally {
      clearTimeout(timer);
      if (externalSignal && !externalSignal.aborted) {
        externalSignal.removeEventListener('abort', onExternalAbort);
      }
    }
  }

  async function requestWithTimeout(fetchFn, url, options, timeoutMs) {
    var requestOptions = Object.assign({}, options || {});
    var externalSignal = requestOptions.signal;
    return runWithTimeout(externalSignal, timeoutMs, function (signal) {
      requestOptions.signal = signal;
      return fetchFn(url, requestOptions);
    });
  }

  async function requestTextWithTimeout(fetchFn, url, options, timeoutMs) {
    var requestOptions = Object.assign({}, options || {});
    var externalSignal = requestOptions.signal;
    return runWithTimeout(externalSignal, timeoutMs, async function (signal) {
      requestOptions.signal = signal;
      var response = await fetchFn(url, requestOptions);
      var text = await response.text();
      return { response: response, text: text };
    });
  }

  async function requestBlobWithTimeout(fetchFn, url, options, timeoutMs) {
    var requestOptions = Object.assign({}, options || {});
    var externalSignal = requestOptions.signal;
    return runWithTimeout(externalSignal, timeoutMs, async function (signal) {
      requestOptions.signal = signal;
      var response = await fetchFn(url, requestOptions);
      var blob = await response.blob();
      return { response: response, blob: blob };
    });
  }

  async function readResponseTextWithTimeout(response, timeoutMs) {
    return runWithTimeout(null, timeoutMs, async function (signal) {
      var cancelBody = function () {
        if (response && response.body && typeof response.body.cancel === 'function') {
          Promise.resolve(response.body.cancel()).catch(function () {});
        }
      };
      signal.addEventListener('abort', cancelBody, { once: true });
      try {
        return await response.text();
      } finally {
        signal.removeEventListener('abort', cancelBody);
      }
    });
  }

  function httpFailure(status, detail) {
    var safeDetail = String(detail || '').trim();
    if (status === 401) {
      return { success: false, status: status, errorCode: 'AUTH_INVALID', error: 'API Key 无效或已失效' };
    }
    if (status === 403) {
      return { success: false, status: status, errorCode: 'AUTH_FORBIDDEN', error: 'API Key 无权访问当前企业知识库' };
    }
    return {
      success: false,
      status: status,
      errorCode: 'HTTP_ERROR',
      error: safeDetail || ('知识库服务返回 HTTP ' + status)
    };
  }

  function fetchFailure(error) {
    if (error && error.code === 'REQUEST_TIMEOUT') {
      return { success: false, errorCode: 'REQUEST_TIMEOUT', error: '请求超时' };
    }
    if (error && error.name === 'AbortError') {
      return { success: false, errorCode: 'REQUEST_CANCELLED', error: '请求已取消' };
    }
    return { success: false, errorCode: 'NETWORK_UNREACHABLE', error: '网络不可达' };
  }

  function invalidResponseFailure() {
    return { success: false, errorCode: 'INVALID_RESPONSE', error: '知识库服务返回了无法识别的响应' };
  }

  function sendRuntimeMessage(chromeApi, data, timeoutMs) {
    var timeout = Number(timeoutMs) > 0 ? Number(timeoutMs) : MESSAGE_TIMEOUT_MS;
    return new Promise(function (resolve) {
      var settled = false;
      var timer;

      function finish(result) {
        if (settled) return;
        settled = true;
        clearTimeout(timer);
        resolve(result);
      }

      timer = setTimeout(function () {
        finish({
          success: false,
          errorCode: 'BACKGROUND_TIMEOUT',
          error: '扩展后台无响应，请重新加载扩展'
        });
      }, timeout);

      try {
        if (!chromeApi || !chromeApi.runtime || typeof chromeApi.runtime.sendMessage !== 'function') {
          finish({ success: false, errorCode: 'BACKGROUND_UNAVAILABLE', error: '扩展后台失联，请重新加载扩展' });
          return;
        }
        chromeApi.runtime.sendMessage(data, function (response) {
          var lastError = chromeApi.runtime.lastError;
          if (lastError) {
            finish({ success: false, errorCode: 'BACKGROUND_UNAVAILABLE', error: '扩展后台失联，请重新加载扩展' });
            return;
          }
          if (response === undefined || response === null) {
            finish({ success: false, errorCode: 'BACKGROUND_UNAVAILABLE', error: '扩展后台失联，请重新加载扩展' });
            return;
          }
          finish(response);
        });
      } catch (error) {
        finish({ success: false, errorCode: 'BACKGROUND_UNAVAILABLE', error: '扩展后台失联，请重新加载扩展' });
      }
    });
  }

  global.JiwaiNetwork = {
    COMPANY_API_BASE: COMPANY_API_BASE,
    REQUEST_TIMEOUT_MS: REQUEST_TIMEOUT_MS,
    MESSAGE_TIMEOUT_MS: MESSAGE_TIMEOUT_MS,
    requestWithTimeout: requestWithTimeout,
    requestTextWithTimeout: requestTextWithTimeout,
    requestBlobWithTimeout: requestBlobWithTimeout,
    readResponseTextWithTimeout: readResponseTextWithTimeout,
    httpFailure: httpFailure,
    fetchFailure: fetchFailure,
    invalidResponseFailure: invalidResponseFailure,
    sendRuntimeMessage: sendRuntimeMessage
  };
})(globalThis);
