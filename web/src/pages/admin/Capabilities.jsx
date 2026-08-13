// ai-form-backend console - AGPL-3.0
import React, { useEffect, useState } from 'react';
import { Card, Table, Button, Toast, Input, InputNumber, Switch } from '@douyinfe/semi-ui';
import { get, put } from '../../api.js';

export default function Capabilities() {
  const [rows, setRows] = useState([]);

  useEffect(() => {
    get('/v1/admin/capability-prices').then((d) => {
      setRows(d.prices.map((p) => ({ ...p, key: p.Capability })));
    }).catch((e) => Toast.error(e.message));
  }, []);

  const patch = (cap, field, value) => {
    setRows((old) => old.map((r) => (r.Capability === cap ? { ...r, [field]: value } : r)));
  };

  const save = async (r) => {
    try {
      await put('/v1/admin/capability-prices/' + r.Capability, {
        credits: r.Credits, model: r.Model, temperature: r.Temperature,
        maxTokens: r.MaxTokens, enabled: r.Enabled,
      });
      Toast.success('已保存(在途请求与预占按快照)');
    } catch (e) { Toast.error(e.message); }
  };

  const columns = [
    { title: '能力', dataIndex: 'Capability', width: 150 },
    { title: '积分/次', dataIndex: 'Credits', render: (v, r) => <InputNumber value={v} onChange={(x) => patch(r.Capability, 'Credits', x)} min={0} style={{ width: 90 }} /> },
    { title: '模型', dataIndex: 'Model', render: (v, r) => <Input value={v} onChange={(x) => patch(r.Capability, 'Model', x)} style={{ width: 170 }} /> },
    { title: '温度', dataIndex: 'Temperature', render: (v, r) => <InputNumber value={v} onChange={(x) => patch(r.Capability, 'Temperature', x)} min={0} max={2} step={0.1} style={{ width: 90 }} /> },
    { title: 'maxTokens', dataIndex: 'MaxTokens', render: (v, r) => <InputNumber value={v} onChange={(x) => patch(r.Capability, 'MaxTokens', x)} min={1} style={{ width: 110 }} /> },
    { title: '启用', dataIndex: 'Enabled', render: (v, r) => <Switch checked={v} onChange={(x) => patch(r.Capability, 'Enabled', x)} /> },
    { title: '', render: (_, r) => <Button size="small" onClick={() => save(r)}>保存</Button> },
  ];

  return (
    <Card className="data-card" title="能力列表" headerExtraContent="模型名对所有上游通用，改动仅影响新请求">
      <Table columns={columns} dataSource={rows} pagination={false} size="small" />
    </Card>
  );
}
