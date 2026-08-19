// ai-form-backend console - AGPL-3.0
import React, { useEffect, useState } from 'react';
import { Plus } from 'lucide-react';
import { get, post, put, del } from '../../api.js';
import { Card, Table, Button, Input, NumberInput, Switch, Confirm, toast } from '../../ui';

export default function Upstreams() {
  const [rows, setRows] = useState([]);
  const [loading, setLoading] = useState(true);
  const [draft, setDraft] = useState({ name: '', baseUrl: '', apiKey: '' });
  const [creating, setCreating] = useState(false);

  const load = () => get('/v1/admin/upstreams').then((d) => {
    setRows(d.upstreams.map((u) => ({ ...u, newKey: '' })));
  }).catch((e) => toast.error(e.message)).finally(() => setLoading(false));

  useEffect(() => { load(); }, []);

  const patch = (id, field, value) => {
    setRows((old) => old.map((r) => (r.id === id ? { ...r, [field]: value } : r)));
  };

  const save = async (r) => {
    try {
      await put('/v1/admin/upstreams/' + r.id, {
        name: r.name, baseUrl: r.baseUrl, apiKey: r.newKey,
        sortOrder: r.sortOrder, enabled: r.enabled,
      });
      toast.success('已保存,即时生效');
      load();
    } catch (e) { toast.error(e.message); }
  };

  const remove = async (r) => {
    try { await del('/v1/admin/upstreams/' + r.id); toast.success('已删除'); load(); }
    catch (e) { toast.error(e.message); }
  };

  const create = async () => {
    setCreating(true);
    try {
      await post('/v1/admin/upstreams', draft);
      setDraft({ name: '', baseUrl: '', apiKey: '' });
      toast.success('已添加');
      load();
    } catch (e) { toast.error(e.message); }
    finally { setCreating(false); }
  };

  const columns = [
    { title: '名称', key: 'name', render: (r) => <Input size="sm" value={r.name} onChange={(x) => patch(r.id, 'name', x)} style={{ width: 120 }} /> },
    { title: 'Base URL(到 /v1 为止)', key: 'baseUrl', render: (r) => <Input size="sm" mono value={r.baseUrl} onChange={(x) => patch(r.id, 'baseUrl', x)} style={{ minWidth: 260 }} /> },
    { title: '密钥(留空不改)', key: 'newKey', render: (r) => <Input size="sm" mono type="password" autoComplete="new-password" value={r.newKey} placeholder={r.apiKeyMasked} onChange={(x) => patch(r.id, 'newKey', x)} style={{ width: 160 }} /> },
    { title: '优先级', key: 'sortOrder', width: 90, render: (r) => <NumberInput size="sm" value={r.sortOrder} onChange={(x) => patch(r.id, 'sortOrder', x ?? 0)} style={{ width: 72 }} /> },
    { title: '启用', key: 'enabled', width: 70, render: (r) => <Switch checked={r.enabled} onChange={(x) => patch(r.id, 'enabled', x)} /> },
    {
      title: '', key: 'ops', render: (r) => (
        <div className="actions">
          <Button size="sm" onClick={() => save(r)}>保存</Button>
          <Confirm title={`删除上游「${r.name}」?`} okText="删除" onConfirm={() => remove(r)}>
            <Button size="sm" variant="danger">删除</Button>
          </Confirm>
        </div>
      ),
    },
  ];

  return (
    <Card flush title="上游列表" extra="按优先级升序故障切换,连续失败自动冷却 60 秒"
      foot={
        <>
          <Input size="sm" placeholder="名称" value={draft.name} onChange={(v) => setDraft({ ...draft, name: v })} style={{ width: 120 }} />
          <Input size="sm" mono placeholder="https://xxx/v1" value={draft.baseUrl} onChange={(v) => setDraft({ ...draft, baseUrl: v })} style={{ flex: 1, minWidth: 220 }} />
          <Input size="sm" mono type="password" autoComplete="new-password" placeholder="sk-..." value={draft.apiKey} onChange={(v) => setDraft({ ...draft, apiKey: v })} style={{ width: 170 }} />
          <Button size="sm" variant="primary" icon={<Plus />} loading={creating} onClick={create} disabled={!draft.name || !draft.baseUrl || !draft.apiKey}>新增上游</Button>
        </>
      }
      note="密钥修改即时生效,无需重启;所有上游统一 OpenAI 格式,模型名在「能力配置」里统一设置。">
      <Table columns={columns} rows={rows} rowKey="id" loading={loading} />
    </Card>
  );
}
