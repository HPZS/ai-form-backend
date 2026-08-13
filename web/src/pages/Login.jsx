// ai-form-backend console - AGPL-3.0
import React, { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { Card, Tabs, TabPane, Form, Button, Toast, Typography } from '@douyinfe/semi-ui';
import { post, setTokens } from '../api.js';

export default function Login() {
  const navigate = useNavigate();
  const [email, setEmail] = useState('');
  const [code, setCode] = useState('');
  const [password, setPassword] = useState('');
  const [sending, setSending] = useState(false);
  const [cooldown, setCooldown] = useState(0);
  const [busy, setBusy] = useState(false);

  const sendCode = async () => {
    if (!email) { Toast.warning('请先填写邮箱'); return; }
    setSending(true);
    try {
      await post('/v1/auth/send-code', { email });
      Toast.success('验证码已发送,请查收邮件');
      let n = 60;
      setCooldown(n);
      const t = setInterval(() => { n -= 1; setCooldown(n); if (n <= 0) clearInterval(t); }, 1000);
    } catch (e) { Toast.error(e.message); }
    finally { setSending(false); }
  };

  const finish = (data) => {
    setTokens(data.accessToken, data.refreshToken);
    navigate('/', { replace: true });
  };

  const loginCode = async () => {
    setBusy(true);
    try { finish(await post('/v1/auth/login', { email, code })); }
    catch (e) { Toast.error(e.message); }
    finally { setBusy(false); }
  };

  const loginPassword = async () => {
    setBusy(true);
    try { finish(await post('/v1/auth/login-password', { email, password })); }
    catch (e) { Toast.error(e.message); }
    finally { setBusy(false); }
  };

  return (
    <div className="login-page">
      <a href="/" className="login-brand">
        <span className="brand-mark" aria-hidden="true"><span /></span>
        <span>AI 智能录入助手</span>
      </a>
      <Card className="login-card" bordered={false}>
        <div className="login-heading">
          <Typography.Title heading={3}>欢迎回来</Typography.Title>
          <Typography.Text type="tertiary">登录你的账户以继续使用控制台</Typography.Text>
        </div>
        <Tabs type="line">
          <TabPane tab="验证码登录" itemKey="code">
            <Form onSubmit={loginCode}>
              <Form.Input field="email" label="邮箱" initValue={email} onChange={setEmail} placeholder="you@example.com" />
              <div style={{ display: 'flex', gap: 8, alignItems: 'flex-end' }}>
                <div style={{ flex: 1 }}>
                  <Form.Input field="code" label="验证码" initValue={code} onChange={setCode} placeholder="6 位数字" />
                </div>
                <Button style={{ marginBottom: 12 }} disabled={cooldown > 0} loading={sending} onClick={sendCode}>
                  {cooldown > 0 ? `${cooldown}s` : '发送验证码'}
                </Button>
              </div>
              <Button theme="solid" htmlType="submit" block loading={busy} size="large">登录 / 注册</Button>
              <Typography.Text type="tertiary" size="small" className="login-tip">
                首次登录自动注册,并赠送 14 天试用积分
              </Typography.Text>
            </Form>
          </TabPane>
          <TabPane tab="密码登录" itemKey="password">
            <Form onSubmit={loginPassword}>
              <Form.Input field="email2" label="邮箱" initValue={email} onChange={setEmail} placeholder="you@example.com" />
              <Form.Input field="password" label="密码" mode="password" initValue={password} onChange={setPassword} />
              <Button theme="solid" htmlType="submit" block loading={busy} size="large">登录</Button>
              <Typography.Text type="tertiary" size="small" className="login-tip">
                忘记密码:用验证码登录后,在个人中心重设
              </Typography.Text>
            </Form>
          </TabPane>
        </Tabs>
      </Card>
      <div className="login-footer">安全、稳定的 AI 智能录入服务</div>
    </div>
  );
}
