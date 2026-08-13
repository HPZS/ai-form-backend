// ai-form-backend console - AGPL-3.0
import React, { useEffect, useState } from 'react';
import { Card, Table, Button, Toast, Input, InputNumber, Switch, Popconfirm, Typography } from '@douyinfe/semi-ui';
import { get, post, put, del } from '../../api.js';

export default function Upstreams() {
  const [rows, setRows] = useState([]);
  const [draft, setDraft] = useState({ name: '', baseUrl: '', apiKey: '' });

  const load = () => get('/v1/admin/upstreams').then((d) => {
    setRows(d.upstreams.map((u) => ({ ...u, key: u.id, newKey: '' })));
  }).catch((e) => Toast.error(e.message));

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
      Toast.success('已保存,即时生效');
      load();
    } catch (e) { Toast.error(e.message); }
  };

  const remove = async (r) => {
    try { await del('/v1/admin/upstreams/' + r.id); Toast.success('已删除'); load(); }
    catch (e) { Toast.error(e.message); }
  };

  const create = async () => {
    try {
      await post('/v1/admin/upstreams', draft);
      setDraft({ name: '', baseUrl: '', apiKey: '' });
      Toast.success('已添加');
      load();
    } catch (e) { Toast.error(e.message); }
  };

  const columns = [
    { title: '名称', dataIndex: 'name', render: (v, r) => <Input value={v} onChange={(x) => patch(r.id, 'name', x)} style={{ width: 110 }} /> },
    { title: 'BaseURL(到 /v1 为止)', dataIndex: 'baseUrl', render: (v, r) => <Input value={v} onChange={(x) => patch(r.id, 'baseUrl', x)} style={{ minWidth: 240 }} /> },
    { title: '密钥(留空不改)', dataIndex: 'newKey', render: (v, r) => <Input value={v} placeholder={r.apiKeyMasked} onChange={(x) => patch(r.id, 'newKey', x)} style={{ width: 150 }} /> },
    { title: '优先级', dataIndex: 'sortOrder', render: (v, r) => <InputNumber value={v} onChange={(x) => patch(r.id, 'sortOrder', x)} style={{ width: 80 }} /> },
    { title: '启用', dataIndex: 'enabled', render: (v, r) => <Switch checked={v} onChange={(x) => patch(r.id, 'enabled', x)} /> },
    {
      title: '', render: (_, r) => (
        <>
          <Button size="small" onClick={() => save(r)} style={{ marginRight: 8 }}>保存</Button>
          <Popconfirm title={`删除上游 ${r.name}?`} onConfirm={() => remove(r)}>
            <Button size="small" type="danger">删除</Button>
          </Popconfirm>
        </>
      ),
    },
  ];

  return (
    <Card className="data-card" title="上游列表" headerExtraContent="按优先级升序故障切换，连续失败自动冷却 60 秒">
      <Table columns={columns} dataSource={rows} pagination={false} size="small" />
      <div className="create-toolbar">
        <Input placeholder="名称" value={draft.name} onChange={(v) => setDraft({ ...draft, name: v })} style={{ width: 120 }} />
        <Input placeholder="https://xxx/v1" value={draft.baseUrl} onChange={(v) => setDraft({ ...draft, baseUrl: v })} style={{ flex: 1, minWidth: 220 }} />
        <Input placeholder="sk-..." value={draft.apiKey} onChange={(v) => setDraft({ ...draft, apiKey: v })} style={{ width: 170 }} />
        <Button theme="solid" onClick={create} disabled={!draft.name || !draft.baseUrl || !draft.apiKey}>新增上游</Button>
      </div>
      <Typography.Text type="tertiary" size="small" className="card-note">
        密钥修改即时生效,无需重启;所有上游统一 OpenAI 格式,模型名在「能力配置」里统一设置。
      </Typography.Text>
    </Card>
  );
}
