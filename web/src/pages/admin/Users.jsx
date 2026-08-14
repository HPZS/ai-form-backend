// ai-form-backend console - AGPL-3.0
import React, { useEffect, useState } from 'react';
import { Card, Table, Button, Toast, Tag, Popconfirm, Modal, InputNumber, Select } from '@douyinfe/semi-ui';
import { get, put, post, api } from '../../api.js';

const TYPE_TAGS = {
  base: { label: '底座', color: 'blue' },
  trial: { label: '试用', color: 'grey' },
  pack: { label: '加油包', color: 'green' },
  bonus: { label: '赠送', color: 'orange' },
};
const typeTag = (v) => TYPE_TAGS[v] || { label: v || '?', color: 'red' };

/** 额度管理弹窗:某用户的桶列表(延期/作废) + 手动发放 */
function CreditsModal({ user, onClose }) {
  const [subs, setSubs] = useState([]);
  const [grant, setGrant] = useState({ planType: 'bonus', credits: 1000, days: 30 });
  const [busy, setBusy] = useState(false);

  const load = () => get(`/v1/admin/users/${user.ID}/subscriptions`)
    .then((d) => setSubs(d.subscriptions))
    .catch((e) => Toast.error(e.message));

  useEffect(() => { load(); }, []); // eslint-disable-line react-hooks/exhaustive-deps

  const doGrant = async () => {
    setBusy(true);
    try {
      await post(`/v1/admin/users/${user.ID}/grants`, grant);
      Toast.success('已发放(用户流水可见)');
      load();
    } catch (e) { Toast.error(e.message); }
    finally { setBusy(false); }
  };

  const extend = async (sub, days) => {
    try {
      await api(`/v1/admin/subscriptions/${sub.id}`, { method: 'PATCH', body: JSON.stringify({ addDays: days }) });
      Toast.success(`已延长 ${days} 天`);
      load();
    } catch (e) { Toast.error(e.message); }
  };

  const revoke = async (sub) => {
    try {
      await api(`/v1/admin/subscriptions/${sub.id}`, { method: 'PATCH', body: JSON.stringify({ revoke: true }) });
      Toast.success('已作废');
      load();
    } catch (e) { Toast.error(e.message); }
  };

  const columns = [
    { title: '来源', dataIndex: 'planName' },
    { title: '类型', dataIndex: 'planType', render: (v) => <Tag color={typeTag(v).color}>{typeTag(v).label}</Tag> },
    { title: '剩余/总额', render: (_, s) => `${s.total - s.used} / ${s.total}` },
    { title: '到期', dataIndex: 'endsAt', render: (v) => new Date(v).toLocaleString('zh-CN') },
    {
      title: '状态', dataIndex: 'status',
      render: (v) => (v === 'active' ? <Tag color="green">生效</Tag> : v === 'expired' ? <Tag color="grey">过期</Tag> : <Tag color="red">作废</Tag>),
    },
    {
      title: '', render: (_, s) => s.status === 'revoked' ? null : (
        <>
          <Button size="small" onClick={() => extend(s, 30)}>+30 天</Button>{' '}
          <Popconfirm title="作废该桶?剩余额度立即不可用。" onConfirm={() => revoke(s)}>
            <Button size="small" type="danger">作废</Button>
          </Popconfirm>
        </>
      ),
    },
  ];

  return (
    <Modal title={`额度管理 · ${user.Email}`} visible onCancel={onClose} footer={null} width={720}>
      <Table columns={columns} dataSource={subs} pagination={false} size="small" rowKey="id" empty="暂无额度桶" />
      <div className="create-toolbar" style={{ marginTop: 16 }}>
        <Select value={grant.planType} onChange={(v) => setGrant({ ...grant, planType: v })} style={{ width: 170 }}
          optionList={[
            { value: 'bonus', label: '赠送(纯积分)' },
            { value: 'base', label: '底座(给订阅资格)' },
            { value: 'pack', label: '加油包(纯积分)' },
          ]} />
        <InputNumber prefix="积分" value={grant.credits} onChange={(v) => setGrant({ ...grant, credits: v })} min={0} style={{ width: 140 }} />
        <InputNumber prefix="天数" value={grant.days} onChange={(v) => setGrant({ ...grant, days: v })} min={1} max={3650} style={{ width: 130 }} />
        <Button theme="solid" loading={busy} onClick={doGrant}>发放</Button>
      </div>
    </Modal>
  );
}

export default function Users() {
  const [rows, setRows] = useState([]);
  const [done, setDone] = useState(false);
  const [loading, setLoading] = useState(false);
  const [creditsUser, setCreditsUser] = useState(null);

  const load = async (beforeId) => {
    setLoading(true);
    try {
      const d = await get('/v1/admin/users' + (beforeId ? `?beforeId=${beforeId}` : ''));
      setRows((old) => (beforeId ? [...old, ...d.users] : d.users));
      if (d.users.length < 100) setDone(true);
    } catch (e) { Toast.error(e.message); }
    finally { setLoading(false); }
  };

  useEffect(() => { load(); }, []); // eslint-disable-line react-hooks/exhaustive-deps

  const setStatus = async (u, status) => {
    try {
      await put('/v1/admin/users/' + u.ID, { status });
      Toast.success(status === 'banned' ? '已封禁(全端登出)' : '已解封');
      setRows((old) => old.map((r) => (r.ID === u.ID ? { ...r, Status: status } : r)));
    } catch (e) { Toast.error(e.message); }
  };

  const columns = [
    { title: 'ID', dataIndex: 'ID', width: 70 },
    { title: '邮箱', dataIndex: 'Email' },
    { title: '角色', dataIndex: 'Role', render: (v) => (v === 'admin' ? <Tag color="blue">管理员</Tag> : '用户') },
    { title: '状态', dataIndex: 'Status', render: (v) => (v === 'active' ? <Tag color="green">正常</Tag> : <Tag color="red">封禁</Tag>) },
    { title: '注册时间', dataIndex: 'CreatedAt', render: (v) => new Date(v).toLocaleString('zh-CN') },
    {
      title: '', render: (_, u) => (
        <>
          <Button size="small" onClick={() => setCreditsUser(u)}>额度</Button>{' '}
          {u.Role === 'admin' ? null : u.Status === 'active' ? (
            <Popconfirm title={`封禁 ${u.Email}?将立即全端登出。`} onConfirm={() => setStatus(u, 'banned')}>
              <Button size="small" type="danger">封禁</Button>
            </Popconfirm>
          ) : (
            <Button size="small" onClick={() => setStatus(u, 'active')}>解封</Button>
          )}
        </>
      ),
    },
  ];

  return (
    <Card className="data-card" title="用户列表">
      <Table columns={columns} dataSource={rows} pagination={false} size="small" rowKey="ID" loading={loading && rows.length === 0} />
      <div className="load-more-row">
        {!done && rows.length > 0 && (
          <Button loading={loading} onClick={() => load(rows[rows.length - 1].ID)}>加载更多</Button>
        )}
      </div>
      {creditsUser && <CreditsModal user={creditsUser} onClose={() => setCreditsUser(null)} />}
    </Card>
  );
}
