// ai-form-backend console - AGPL-3.0
// API 封装:Bearer access token + 401 时自动用 refresh token 旋转换新。

let access = localStorage.getItem('afb_access') || '';
let refresh = localStorage.getItem('afb_refresh') || '';

export function setTokens(a, r) {
  access = a; refresh = r;
  localStorage.setItem('afb_access', a);
  localStorage.setItem('afb_refresh', r);
}

export function clearTokens() {
  access = refresh = '';
  localStorage.removeItem('afb_access');
  localStorage.removeItem('afb_refresh');
}

export function hasTokens() { return !!refresh; }

export async function api(path, opts = {}, retried = false) {
  const headers = { 'Content-Type': 'application/json', ...(opts.headers || {}) };
  if (access) headers['Authorization'] = 'Bearer ' + access;
  const res = await fetch(path, { ...opts, headers });
  if (res.status === 401 && refresh && !retried && path !== '/v1/auth/refresh') {
    const r = await fetch('/v1/auth/refresh', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ refreshToken: refresh }),
    });
    if (r.ok) {
      const pair = await r.json();
      setTokens(pair.accessToken, pair.refreshToken);
      return api(path, opts, true);
    }
    clearTokens();
    window.location.href = '/login';
    throw new Error('登录已失效');
  }
  if (res.status === 204) return null;
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    const err = new Error(data.message || data.error || 'HTTP ' + res.status);
    err.code = data.error;
    err.data = data;
    throw err;
  }
  return data;
}

export const get = (p) => api(p);
export const post = (p, body) => api(p, { method: 'POST', body: JSON.stringify(body ?? {}) });
export const put = (p, body) => api(p, { method: 'PUT', body: JSON.stringify(body ?? {}) });
export const del = (p) => api(p, { method: 'DELETE' });

export async function logout() {
  if (refresh) {
    try { await post('/v1/auth/logout', { refreshToken: refresh }); } catch { /* 忽略,本地照常清除 */ }
  }
  clearTokens();
  window.location.href = '/login';
}
