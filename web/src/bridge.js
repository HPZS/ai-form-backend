// ai-form-backend console - AGPL-3.0
// 浏览器插件桥接:插件的内容脚本在本页发 {source:'aiform-ext', type:'sso-request'}
// (插件未登录时);本页若已登录,就向后端换一个 60 秒单次使用的登录码递回去,
// 插件用它建立自己的会话——实现"网站登录了,插件自动登录"。
import { hasTokens, post } from './api.js';

let sending = false;

async function sendSsoCode() {
  if (sending || !hasTokens()) return;
  sending = true;
  try {
    const d = await post('/v1/auth/sso-code');
    window.postMessage({ source: 'aiform-console', type: 'sso-code', code: d.code }, window.location.origin);
  } catch {
    // 未登录/限频等:不递码即可,插件保持未登录
  } finally {
    sending = false;
  }
}

window.addEventListener('message', (e) => {
  if (e.origin !== window.location.origin) return;
  if (e.data?.source === 'aiform-ext' && e.data.type === 'sso-request') void sendSsoCode();
});

/** 登录成功后主动推一次(插件的握手可能已经停了) */
export function offerSsoToExtension() {
  void sendSsoCode();
}
