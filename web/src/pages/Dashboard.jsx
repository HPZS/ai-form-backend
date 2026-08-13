// ai-form-backend console - AGPL-3.0
// 个人中心:可用积分、订阅桶、账号信息与密码设置。
import React, { useContext, useState } from 'react';
import { Card, Descriptions, Table, Typography, Input, Button, Toast, Row, Col } from '@douyinfe/semi-ui';
import { MeContext } from '../App.jsx';
import { post } from '../api.js';

export default function Dashboard() {
  const { me } = useContext(MeContext);
  const [pw, setPw] = useState('');
  const [saving, setSaving] = useState(false);

  const setPassword = async () => {
    setSaving(true);
    try {
      await post('/v1/auth/set-password', { password: pw });
      setPw('');
      Toast.success('密码已设置,下次可直接密码登录');
    } catch (e) { Toast.error(e.message); }
    finally { setSaving(false); }
  };

  const columns = [
    { title: '额度', dataIndex: 'total' },
    { title: '剩余', dataIndex: 'remaining', render: (v) => <Typography.Text strong>{v}</Typography.Text> },
    { title: '到期时间', dataIndex: 'endsAt', render: (v) => new Date(v).toLocaleString('zh-CN') },
  ];

  return (
    <Row gutter={16}>
      <Col span={24} style={{ marginBottom: 16 }}>
        <Card>
          <Descriptions row size="large" data={[
            { key: '可用积分', value: <Typography.Title heading={2} style={{ color: 'var(--semi-color-primary)' }}>{me.available}</Typography.Title> },
            { key: '邮箱', value: me.email },
            { key: '角色', value: me.role === 'admin' ? '管理员' : '普通用户' },
          ]} />
        </Card>
      </Col>
      <Col span={14}>
        <Card title="我的订阅(到期后剩余积分清零,历史保留)">
          <Table columns={columns} dataSource={me.buckets} pagination={false} rowKey={(r) => r.endsAt + '-' + r.total} size="small"
            empty="暂无有效订阅,去「购买套餐」开通" />
        </Card>
      </Col>
      <Col span={10}>
        <Card title="设置登录密码">
          <Typography.Paragraph type="tertiary" size="small">
            设置后可用「邮箱 + 密码」直接登录;忘记密码时用验证码登录再回这里重设。
          </Typography.Paragraph>
          <div style={{ display: 'flex', gap: 8 }}>
            <Input mode="password" value={pw} onChange={setPw} placeholder="新密码(至少 8 位)" />
            <Button theme="solid" loading={saving} disabled={pw.length < 8} onClick={setPassword}>保存</Button>
          </div>
        </Card>
      </Col>
    </Row>
  );
}
