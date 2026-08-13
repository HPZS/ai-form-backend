// ai-form-backend console - AGPL-3.0
// 能力配置:全局默认参数统一配,单个能力只在需要时覆盖(留空 = 用默认)。
import React, { useEffect, useState } from 'react';
import { Card, Table, Button, Toast, Input, InputNumber, Switch, Typography, Banner } from '@douyinfe/semi-ui';
import { get, put } from '../../api.js';

/** Semi 数字输入清空时给 ''/undefined:统一归一成 null(= 用默认) */
const numOrNull = (x) => (x === '' || x === null || x === undefined ? null : x);

export default function Capabilities() {
  const [defaults, setDefaults] = useState({ model: '', temperature: 0, maxTokens: 4000 });
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
      Toast.success('默认参数已保存,即时生效');
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
        temperature: numOrNull(r.temperature), maxTokens: numOrNull(r.maxTokens),
      });
      Toast.success(`「${r.name}」已保存(在途请求与预占按快照)`);
    } catch (e) { Toast.error(e.message); }
  };

  const columns = [
    {
      title: '能力', dataIndex: 'name', width: 220,
      render: (v, r) => (
        <div>
          <div style={{ fontWeight: 600 }}>{v}</div>
          <Typography.Text type="tertiary" size="small">{r.desc}</Typography.Text>
        </div>
      ),
    },
    { title: '积分/次', dataIndex: 'credits', width: 110, render: (v, r) => <InputNumber value={v} onChange={(x) => patch(r.capability, 'credits', numOrNull(x) ?? 0)} min={0} style={{ width: 90 }} /> },
    { title: '模型(留空用默认)', dataIndex: 'model', width: 190, render: (v, r) => <Input value={v} onChange={(x) => patch(r.capability, 'model', x)} placeholder={`默认:${defaults.model || '未设置'}`} style={{ width: 170 }} /> },
    { title: '温度(留空用默认)', dataIndex: 'temperature', width: 140, render: (v, r) => <InputNumber value={v ?? ''} onChange={(x) => patch(r.capability, 'temperature', numOrNull(x))} min={0} max={2} step={0.1} placeholder={`默认:${defaults.temperature}`} style={{ width: 120 }} /> },
    { title: 'maxTokens(留空用默认)', dataIndex: 'maxTokens', width: 160, render: (v, r) => <InputNumber value={v ?? ''} onChange={(x) => patch(r.capability, 'maxTokens', numOrNull(x))} min={1} placeholder={`默认:${defaults.maxTokens}`} style={{ width: 130 }} /> },
    { title: '启用', dataIndex: 'enabled', width: 70, render: (v, r) => <Switch checked={v} onChange={(x) => patch(r.capability, 'enabled', x)} /> },
    { title: '', width: 80, render: (_, r) => <Button size="small" onClick={() => saveRow(r)}>保存</Button> },
  ];

  return (
    <>
      <Card className="data-card" title="默认参数" style={{ marginBottom: 16 }}>
        {!defaults.model && (
          <Banner type="warning" closeIcon={null} style={{ marginBottom: 12 }}
            description="还没设置默认模型,所有 AI 能力都无法调用。填一个上游支持的模型名并保存。" />
        )}
        <div style={{ display: 'flex', gap: 16, alignItems: 'flex-end', flexWrap: 'wrap' }}>
          <div>
            <Typography.Text strong style={{ display: 'block', marginBottom: 4 }}>模型</Typography.Text>
            <Input value={defaults.model} onChange={(x) => setDefaults((d) => ({ ...d, model: x }))} placeholder="上游支持的模型名,对所有上游通用" style={{ width: 240 }} />
          </div>
          <div>
            <Typography.Text strong style={{ display: 'block', marginBottom: 4 }}>温度</Typography.Text>
            <InputNumber value={defaults.temperature} onChange={(x) => setDefaults((d) => ({ ...d, temperature: numOrNull(x) ?? 0 }))} min={0} max={2} step={0.1} style={{ width: 100 }} />
          </div>
          <div>
            <Typography.Text strong style={{ display: 'block', marginBottom: 4 }}>maxTokens</Typography.Text>
            <InputNumber value={defaults.maxTokens} onChange={(x) => setDefaults((d) => ({ ...d, maxTokens: numOrNull(x) ?? 4000 }))} min={1} style={{ width: 110 }} />
          </div>
          <Button theme="solid" loading={savingDef} onClick={saveDefaults}>保存默认参数</Button>
        </div>
        <Typography.Text type="tertiary" size="small" style={{ display: 'block', marginTop: 10 }}>
          所有能力默认使用这里的参数;下方某个能力单独填了值才用它自己的。改动即时生效,只影响之后的请求。
        </Typography.Text>
      </Card>
      <Card className="data-card" title="能力列表">
        <Table columns={columns} dataSource={rows} pagination={false} size="small" />
      </Card>
    </>
  );
}
