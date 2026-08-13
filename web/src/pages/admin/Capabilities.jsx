// ai-form-backend console - AGPL-3.0
// 能力配置:默认模型统一配一次,单个能力只在需要时覆盖模型(留空 = 用默认)。
// 温度/maxTokens 由后端代码按能力定死,不是运营配置项。
import React, { useEffect, useState } from 'react';
import { Card, Table, Button, Toast, Input, InputNumber, Switch, Typography, Banner } from '@douyinfe/semi-ui';
import { get, put } from '../../api.js';

export default function Capabilities() {
  const [defaults, setDefaults] = useState({ model: '' });
  const [rows, setRows] = useState([]);
  const [savingDef, setSavingDef] = useState(false);

  const load = async () => {
    try {
      const [d, p] = await Promise.all([get('/v1/admin/ai-defaults'), get('/v1/admin/capability-prices')]);
      setDefaults(d);
      setRows(p.prices.map((x) => ({ ...x, key: x.capability })));
    } catch (e) { Toast.error(e.message); }
  };
  useEffect(() => { load(); }, []); // eslint-disable-line react-hooks/exhaustive-deps

  const saveDefaults = async () => {
    if (!defaults.model || !defaults.model.trim()) { Toast.warning('默认模型不能为空'); return; }
    setSavingDef(true);
    try {
      await put('/v1/admin/ai-defaults', defaults);
      Toast.success('默认模型已保存,即时生效');
    } catch (e) { Toast.error(e.message); }
    finally { setSavingDef(false); }
  };

  const patch = (cap, field, value) => {
    setRows((old) => old.map((r) => (r.capability === cap ? { ...r, [field]: value } : r)));
  };

  const saveRow = async (r) => {
    try {
      await put('/v1/admin/capability-prices/' + r.capability, {
        credits: r.credits, enabled: r.enabled, model: r.model || '',
      });
      Toast.success(`「${r.name}」已保存(在途请求与预占按快照)`);
    } catch (e) { Toast.error(e.message); }
  };

  const columns = [
    {
      title: '能力', dataIndex: 'name', width: 240,
      render: (v, r) => (
        <div>
          <div style={{ fontWeight: 600 }}>{v}</div>
          <Typography.Text type="tertiary" size="small">{r.desc}</Typography.Text>
        </div>
      ),
    },
    { title: '积分/次', dataIndex: 'credits', width: 110, render: (v, r) => <InputNumber value={v} onChange={(x) => patch(r.capability, 'credits', x === '' || x == null ? 0 : x)} min={0} style={{ width: 90 }} /> },
    { title: '模型(留空用默认)', dataIndex: 'model', width: 210, render: (v, r) => <Input value={v} onChange={(x) => patch(r.capability, 'model', x)} placeholder={`默认:${defaults.model || '未设置'}`} style={{ width: 190 }} /> },
    { title: '启用', dataIndex: 'enabled', width: 70, render: (v, r) => <Switch checked={v} onChange={(x) => patch(r.capability, 'enabled', x)} /> },
    { title: '', width: 80, render: (_, r) => <Button size="small" onClick={() => saveRow(r)}>保存</Button> },
  ];

  return (
    <>
      <Card className="data-card" title="默认模型" style={{ marginBottom: 16 }}>
        {!defaults.model && (
          <Banner type="warning" closeIcon={null} style={{ marginBottom: 12 }}
            description="还没设置默认模型,所有 AI 能力都无法调用。填一个上游支持的模型名并保存。" />
        )}
        <div style={{ display: 'flex', gap: 16, alignItems: 'flex-end', flexWrap: 'wrap' }}>
          <div>
            <Typography.Text strong style={{ display: 'block', marginBottom: 4 }}>模型</Typography.Text>
            <Input value={defaults.model} onChange={(x) => setDefaults((d) => ({ ...d, model: x }))} placeholder="上游支持的模型名,对所有上游通用" style={{ width: 260 }} />
          </div>
          <Button theme="solid" loading={savingDef} onClick={saveDefaults}>保存</Button>
        </div>
        <Typography.Text type="tertiary" size="small" style={{ display: 'block', marginTop: 10 }}>
          所有能力默认使用这个模型;下方某个能力单独填了模型才用它自己的。改动即时生效,只影响之后的请求。
        </Typography.Text>
      </Card>
      <Card className="data-card" title="能力列表">
        <Table columns={columns} dataSource={rows} pagination={false} size="small" />
      </Card>
    </>
  );
}
