// ai-form-backend console - AGPL-3.0
import React, { useEffect, useState } from 'react';
import { get, put, post, api } from '../../api.js';
import { Card, Table, LoadMore, Button, Tag, Confirm, Dialog, NumberInput, Select, toast, fmtTime, fmtNum } from '../../ui';

const TYPE_TAGS = {
  base: { label: '底座', tone: 'accent' },
  trial: { label: '试用', tone: 'neutral' },
  pack: { label: '加油包', tone: 'ok' },
  bonus: { label: '赠送', tone: 'warn' },
};
const typeTag = (v) => TYPE_TAGS[v] || { label: v || '?', tone: 'danger' };
const SUB_STATUS = {
  active: <Tag tone="ok" dot>生效</Tag>,
  expired: <Tag dot>过期</Tag>,
  revoked: <Tag tone="danger" dot>作废</Tag>,
};

/** 额度管理弹窗:某用户的桶列表(延期/作废) + 手动发放 */
function CreditsDialog({ user, onClose }) {
  const [subs, setSubs] = useState([]);
  const [loading, setLoading] = useState(true);
  const [grant, setGrant] = useState({ planType: 'bonus', credits: 1000, days: 30 });
  const [busy, setBusy] = useState(false);

  const load = () => get(`/v1/admin/users/${user.ID}/subscriptions`)
    .then((d) => setSubs(d.subscriptions))
    .catch((e) => toast.error(e.message))
    .finally(() => setLoading(false));

  useEffect(() => { load(); }, []); // eslint-disable-line react-hooks/exhaustive-deps

  const doGrant = async () => {
    setBusy(true);
    try {
      await post(`/v1/admin/users/${user.ID}/grants`, grant);
      toast.success('已发放(用户流水可见)');
      load();
    } catch (e) { toast.error(e.message); }
    finally { setBusy(false); }
  };

  const extend = async (sub, days) => {
    try {
      await api(`/v1/admin/subscriptions/${sub.id}`, { method: 'PATCH', body: JSON.stringify({ addDays: days }) });
      toast.success(`已延长 ${days} 天`);
      load();
    } catch (e) { toast.error(e.message); }
  };

  const revoke = async (sub) => {
    try {
      await api(`/v1/admin/subscriptions/${sub.id}`, { method: 'PATCH', body: JSON.stringify({ revoke: true }) });
      toast.success('已作废');
      load();
    } catch (e) { toast.error(e.message); }
  };

  const columns = [
    { title: '来源', key: 'planName', render: (r) => <span className="cell-title">{r.planName}</span> },
    { title: '类型', key: 'planType', render: (r) => <Tag tone={typeTag(r.planType).tone}>{typeTag(r.planType).label}</Tag> },
    { title: '剩余 / 总额', key: 'amount', align: 'right', render: (r) => <span className="num"><b>{fmtNum(r.total - r.used)}</b> <span className="muted">/ {fmtNum(r.total)}</span></span> },
    { title: '到期', key: 'endsAt', render: (r) => <span className="num muted">{fmtTime(r.endsAt)}</span> },
    { title: '状态', key: 'status', render: (r) => SUB_STATUS[r.status] || <Tag>{r.status}</Tag> },
    {
      title: '', key: 'ops', render: (r) => r.status === 'revoked' ? null : (
        <div className="actions">
          <Button size="sm" onClick={() => extend(r, 30)}>+30 天</Button>
          <Confirm title="作废该桶?剩余额度立即不可用。" okText="作废" onConfirm={() => revoke(r)}>
            <Button size="sm" variant="danger">作废</Button>
          </Confirm>
        </div>
      ),
    },
  ];

  return (
    <Dialog open title={`额度管理 · ${user.Email}`} width={760} onClose={onClose}>
      <Table columns={columns} rows={subs} rowKey="id" loading={loading} empty="暂无额度桶" />
      <div className="grant-bar">
        <span className="field-label">手动发放</span>
        <Select size="sm" value={grant.planType} onChange={(v) => setGrant({ ...grant, planType: v })} style={{ width: 170 }}
          options={[
            { value: 'bonus', label: '赠送(纯积分)' },
            { value: 'base', label: '底座(给订阅资格)' },
            { value: 'pack', label: '加油包(纯积分)' },
          ]} />
        <NumberInput size="sm" prefix="积分" value={grant.credits} onChange={(v) => setGrant({ ...grant, credits: v ?? 0 })} min={0} style={{ width: 130 }} />
        <NumberInput size="sm" prefix="天数" value={grant.days} onChange={(v) => setGrant({ ...grant, days: v ?? 1 })} min={1} max={3650} style={{ width: 120 }} />
        <Button size="sm" variant="primary" loading={busy} onClick={doGrant}>发放</Button>
      </div>
    </Dialog>
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
    } catch (e) { toast.error(e.message); }
    finally { setLoading(false); }
  };

  useEffect(() => { load(); }, []); // eslint-disable-line react-hooks/exhaustive-deps

  const setStatus = async (u, status) => {
    try {
      await put('/v1/admin/users/' + u.ID, { status });
      toast.success(status === 'banned' ? '已封禁(全端登出)' : '已解封');
      setRows((old) => old.map((r) => (r.ID === u.ID ? { ...r, Status: status } : r)));
    } catch (e) { toast.error(e.message); }
  };

  const columns = [
    { title: 'ID', key: 'ID', width: 70, render: (r) => <span className="mono muted">{r.ID}</span> },
    { title: '邮箱', key: 'Email', render: (r) => <span className="cell-title">{r.Email}</span> },
    { title: '角色', key: 'Role', render: (r) => (r.Role === 'admin' ? <Tag tone="accent">管理员</Tag> : <span className="muted">用户</span>) },
    { title: '状态', key: 'Status', render: (r) => (r.Status === 'active' ? <Tag tone="ok" dot>正常</Tag> : <Tag tone="danger" dot>封禁</Tag>) },
    { title: '注册时间', key: 'CreatedAt', render: (r) => <span className="num muted">{fmtTime(r.CreatedAt)}</span> },
    {
      title: '', key: 'ops', render: (r) => (
        <div className="actions">
          <Button size="sm" onClick={() => setCreditsUser(r)}>额度</Button>
          {r.Role === 'admin' ? null : r.Status === 'active' ? (
            <Confirm title={`封禁 ${r.Email}?将立即全端登出。`} okText="封禁" onConfirm={() => setStatus(r, 'banned')}>
              <Button size="sm" variant="danger">封禁</Button>
            </Confirm>
          ) : (
            <Button size="sm" onClick={() => setStatus(r, 'active')}>解封</Button>
          )}
        </div>
      ),
    },
  ];

  return (
    <Card flush title="用户列表" extra={rows.length > 0 ? `已加载 ${rows.length} 人` : null}>
      <Table columns={columns} rows={rows} rowKey="ID" loading={loading} />
      <LoadMore done={done} loading={loading} count={rows.length} onClick={() => load(rows[rows.length - 1].ID)} />
      {creditsUser && <CreditsDialog user={creditsUser} onClose={() => setCreditsUser(null)} />}
    </Card>
  );
}
