(function (global) {
  'use strict';

  function validationErrorMessage(result) {
    var code = result && result.errorCode;
    if (code === 'AUTH_INVALID' || result && result.status === 401) return 'API Key 无效或已失效';
    if (code === 'AUTH_FORBIDDEN' || result && result.status === 403) return 'API Key 权限不足，请联系管理员';
    if (code === 'REQUEST_TIMEOUT') return '验证超时，请稍后重试';
    if (code === 'NETWORK_UNREACHABLE') return '网络不可达，请检查网络连接';
    if (code === 'BACKGROUND_TIMEOUT') return '扩展后台无响应，请重新加载扩展';
    if (code === 'BACKGROUND_UNAVAILABLE') return '扩展后台失联，请在扩展管理页重新加载';
    if (code === 'INVALID_RESPONSE') return '知识库服务响应异常，请稍后重试';
    if (result && result.status >= 500) return '知识库服务暂时异常（HTTP ' + result.status + '）';
    return result && result.error ? result.error : '验证失败，请稍后重试';
  }

  async function validateCompanyConnection(options) {
    var apiKey = String(options.apiKey || '').trim();
    var sendMessage = options.sendMessage;
    var button = options.button;
    var statusElement = options.statusElement;
    var originalText = button ? button.textContent : '';

    if (!apiKey) {
      if (statusElement) {
        statusElement.textContent = '请填写 API Key';
        statusElement.className = 'wk-test-result err';
      }
      return { success: false, errorCode: 'API_KEY_REQUIRED', error: '请填写 API Key' };
    }

    if (button) {
      button.disabled = true;
      button.textContent = '正在验证…';
    }
    if (statusElement) {
      statusElement.textContent = '正在验证…';
      statusElement.className = 'wk-test-result';
    }

    try {
      var saved = await sendMessage({
        type: 'SET_CONFIG',
        payload: { baseUrl: global.JiwaiNetwork.COMPANY_API_BASE, apiKey: apiKey }
      });
      if (!saved || !saved.success) {
        var saveMessage = validationErrorMessage(saved);
        if (statusElement) {
          statusElement.textContent = saveMessage;
          statusElement.className = 'wk-test-result err';
        }
        return saved || { success: false, errorCode: 'BACKGROUND_UNAVAILABLE' };
      }

      var result = await sendMessage({ type: 'VALIDATE_CONFIG' });
      if (!result || !result.success) {
        var message = validationErrorMessage(result);
        if (statusElement) {
          statusElement.textContent = message;
          statusElement.className = 'wk-test-result err';
        }
        return result || { success: false, errorCode: 'BACKGROUND_UNAVAILABLE' };
      }

      var authenticated = await sendMessage({
        type: 'SET_AUTH',
        payload: { type: 'wk', name: '见外知识库用户', avatar: '' }
      });
      if (!authenticated || !authenticated.success) {
        var authMessage = validationErrorMessage(authenticated);
        if (statusElement) {
          statusElement.textContent = authMessage;
          statusElement.className = 'wk-test-result err';
        }
        return authenticated || { success: false, errorCode: 'BACKGROUND_UNAVAILABLE' };
      }
      if (statusElement) {
        statusElement.textContent = '验证通过，配置已保存';
        statusElement.className = 'wk-test-result ok';
      }
      return authenticated;
    } catch (error) {
      var fallback = { success: false, errorCode: 'BACKGROUND_UNAVAILABLE', error: '扩展后台失联，请重新加载扩展' };
      if (statusElement) {
        statusElement.textContent = validationErrorMessage(fallback);
        statusElement.className = 'wk-test-result err';
      }
      return fallback;
    } finally {
      if (button) {
        button.disabled = false;
        button.textContent = originalText;
      }
    }
  }

  global.JiwaiPopupAuth = {
    validationErrorMessage: validationErrorMessage,
    validateCompanyConnection: validateCompanyConnection
  };
})(globalThis);
