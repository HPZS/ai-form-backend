// ai-form-backend console - AGPL-3.0
// SSO 落地页:插件带一次性登录码打开本页(#code=...&next=/topup),换取会话后跳转。
// 码放 URL fragment,不会出现在服务端访问日志里。
import React, { useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { Spin, Typography, Button } from '@douyinfe/semi-ui';
import { post, setTokens } from '../api.js';

export default function Sso() {
  const navigate = useNavigate();
  const [error, setError] = useState('');

  useEffect(() => {
    const params = new URLSearchParams(window.location.hash.slice(1));
    const code = params.get('code');
    const next = params.get('next') || '/topup';
    // 立即清掉地址栏里的码,防止停留期间被复制/截图泄露
    window.history.replaceState(null, '', '/sso');
    if (!code) { setError('缺少登录码'); return; }
    post('/v1/auth/sso-exchange', { code })
      .then((d) => {
        setTokens(d.accessToken, d.refreshToken);
        navigate(next.startsWith('/') ? next : '/topup', { replace: true });
      })
      .catch((e) => setError(e.message || '登录码无效或已过期'));
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  if (error) {
    return (
      <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', marginTop: 120, gap: 12 }}>
        <Typography.Title heading={4}>自动登录失败</Typography.Title>
        <Typography.Text type="tertiary">{error}(登录码 60 秒内有效且只能用一次,请回插件重新点击)</Typography.Text>
        <Button theme="solid" onClick={() => navigate('/login')}>手动登录</Button>
      </div>
    );
  }
  return (
    <div style={{ display: 'flex', justifyContent: 'center', marginTop: 120 }}>
      <Spin size="large" tip="正在为你自动登录..." />
    </div>
  );
}
