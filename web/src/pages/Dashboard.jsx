// ai-form-backend console - AGPL-3.0
// 个人中心:可用积分、订阅桶、账号信息与密码设置。
import React, { useContext, useState } from 'react';
import { Card, Table, Typography, Input, Button, Toast, Row, Col, Tag } from '@douyinfe/semi-ui';
import { IconCreditCard, IconMail, IconUser } from '@douyinfe/semi-icons';
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
    <Row gutter={[16, 16]} className="dashboard-grid">
      <Col xs={24} sm={12} lg={8}>
        <Card className="stat-card">
          <div className="stat-card-head"><span>可用积分</span><span className="stat-icon blue"><IconCreditCard /></span></div>
          <Typography.Title heading={2}>{me.available}</Typography.Title>
          <Typography.Text type="tertiary">当前账户可消费额度</Typography.Text>
        </Card>
      </Col>
      <Col xs={24} sm={12} lg={8}>
        <Card className="stat-card">
          <div className="stat-card-head"><span>登录邮箱</span><span className="stat-icon violet"><IconMail /></span></div>
          <Typography.Title heading={5} ellipsis={{ showTooltip: true }}>{me.email}</Typography.Title>
          <Typography.Text type="tertiary">账号身份标识</Typography.Text>
        </Card>
      </Col>
      <Col xs={24} sm={12} lg={8}>
        <Card className="stat-card">
          <div className="stat-card-head"><span>账户角色</span><span className="stat-icon green"><IconUser /></span></div>
          <div className="stat-role"><Tag color={me.role === 'admin' ? 'blue' : 'green'}>{me.role === 'admin' ? '管理员' : '普通用户'}</Tag></div>
          <Typography.Text type="tertiary">当前账号权限级别</Typography.Text>
        </Card>
      </Col>
      <Col xs={24} lg={15}>
        <Card title="我的订阅" headerExtraContent={<Typography.Text type="tertiary" size="small">到期后剩余积分清零，历史保留</Typography.Text>}>
          <Table columns={columns} dataSource={me.buckets} pagination={false} rowKey={(r) => r.endsAt + '-' + r.total} size="small"
            empty="暂无有效订阅,去「购买套餐」开通" />
        </Card>
      </Col>
      <Col xs={24} lg={9}>
        <Card title="设置登录密码">
          <Typography.Paragraph type="tertiary" size="small">
            设置后可用「邮箱 + 密码」直接登录;忘记密码时用验证码登录再回这里重设。
          </Typography.Paragraph>
          <div className="inline-form">
            <Input mode="password" value={pw} onChange={setPw} placeholder="新密码(至少 8 位)" />
            <Button theme="solid" loading={saving} disabled={pw.length < 8} onClick={setPassword}>保存</Button>
          </div>
        </Card>
      </Col>
    </Row>
  );
}
