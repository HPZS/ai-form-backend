// ai-form-backend console - AGPL-3.0
import React, { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { post, setTokens } from '../api.js';
import { offerSsoToExtension } from '../bridge.js';
import { Brand } from '../components/Layout.jsx';
import { Button, Input, Field, Segmented, toast } from '../ui';

export default function Login() {
  const navigate = useNavigate();
  const [mode, setMode] = useState('code');
  const [email, setEmail] = useState('');
  const [code, setCode] = useState('');
  const [password, setPassword] = useState('');
  const [sending, setSending] = useState(false);
  const [cooldown, setCooldown] = useState(0);
  const [busy, setBusy] = useState(false);

  const sendCode = async () => {
    if (!email) { toast.warning('请先填写邮箱'); return; }
    setSending(true);
    try {
      await post('/v1/auth/send-code', { email });
      toast.success('验证码已发送,请查收邮件');
      let n = 60;
      setCooldown(n);
      const t = setInterval(() => { n -= 1; setCooldown(n); if (n <= 0) clearInterval(t); }, 1000);
    } catch (e) { toast.error(e.message); }
    finally { setSending(false); }
  };

  const finish = (data) => {
    setTokens(data.accessToken, data.refreshToken);
    offerSsoToExtension(); // 插件在场且未登录时,登录态即刻同步过去
    navigate('/', { replace: true });
  };

  const submit = async (e) => {
    e.preventDefault();
    setBusy(true);
    try {
      finish(mode === 'code'
        ? await post('/v1/auth/login', { email, code })
        : await post('/v1/auth/login-password', { email, password }));
    } catch (err) { toast.error(err.message); }
    finally { setBusy(false); }
  };

  return (
    <div className="login">
      <a href="/" className="login-top"><Brand /></a>
      <div className="login-card">
        <div className="login-heading">
          <h1>登录</h1>
          <p>欢迎回来,继续使用 AI 智能录入助手</p>
        </div>
        <Segmented block value={mode} onChange={setMode}
          options={[{ value: 'code', label: '验证码登录' }, { value: 'password', label: '密码登录' }]} />
        <form className="login-form" onSubmit={submit}>
          <Field label="邮箱">
            <Input size="lg" type="email" autoComplete="email" value={email} onChange={setEmail} placeholder="you@example.com" required />
          </Field>
          {mode === 'code' ? (
            <Field label="验证码">
              <Input size="lg" inputMode="numeric" autoComplete="one-time-code" value={code} onChange={setCode} placeholder="6 位数字" required
                suffix={
                  <Button variant="ghost" size="sm" disabled={cooldown > 0} loading={sending} onClick={sendCode}>
                    {cooldown > 0 ? `${cooldown}s 后重发` : '发送验证码'}
                  </Button>
                } />
            </Field>
          ) : (
            <Field label="密码">
              <Input size="lg" type="password" autoComplete="current-password" value={password} onChange={setPassword} placeholder="••••••••" required />
            </Field>
          )}
          <Button type="submit" variant="primary" size="lg" block loading={busy}>
            {mode === 'code' ? '登录 / 注册' : '登录'}
          </Button>
          <p className="login-hint">
            {mode === 'code' ? '首次登录自动注册,并赠送 14 天试用积分' : '忘记密码:用验证码登录后,在个人中心重设'}
          </p>
        </form>
      </div>
      <p className="login-foot">安全、稳定的 AI 智能录入服务</p>
    </div>
  );
}
