// ai-form-backend console - AGPL-3.0
// 能力配置:默认模型统一配一次,单个能力只在需要时覆盖模型(留空 = 用默认)。
// 温度/maxTokens 由后端代码按能力定死,不是运营配置项。
import React, { useEffect, useState } from 'react';
import { get, put } from '../../api.js';
import { Card, Table, Button, Input, NumberInput, Switch, Notice, toast } from '../../ui';

export default function Capabilities() {
  const [defaults, setDefaults] = useState({ model: '' });
  const [rows, setRows] = useState([]);
  const [loading, setLoading] = useState(true);
  const [savingDef, setSavingDef] = useState(false);

  const load = async () => {
    try {
      const [d, p] = await Promise.all([get('/v1/admin/ai-defaults'), get('/v1/admin/capability-prices')]);
      setDefaults(d);
      setRows(p.prices);
    } catch (e) { toast.error(e.message); }
    finally { setLoading(false); }
  };
  useEffect(() => { load(); }, []); // eslint-disable-line react-hooks/exhaustive-deps

  const saveDefaults = async () => {
    if (!defaults.model || !defaults.model.trim()) { toast.warning('默认模型不能为空'); return; }
    setSavingDef(true);
    try {
      await put('/v1/admin/ai-defaults', defaults);
      toast.success('默认模型已保存,即时生效');
    } catch (e) { toast.error(e.message); }
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
      toast.success(`「${r.name}」已保存(在途请求与预占按快照)`);
    } catch (e) { toast.error(e.message); }
  };

  const columns = [
    {
      title: '能力', key: 'name',
      render: (r) => (
        <div>
          <div className="cell-title">{r.name}</div>
          <div className="cell-sub">{r.desc}</div>
        </div>
      ),
    },
    { title: '积分/次', key: 'credits', width: 110, render: (r) => <NumberInput size="sm" value={r.credits} onChange={(x) => patch(r.capability, 'credits', x ?? 0)} min={0} style={{ width: 88 }} /> },
    { title: '模型(留空用默认)', key: 'model', width: 230, render: (r) => <Input size="sm" mono value={r.model} onChange={(x) => patch(r.capability, 'model', x)} placeholder={defaults.model || '未设置默认'} style={{ width: 210 }} /> },
    { title: '启用', key: 'enabled', width: 70, render: (r) => <Switch checked={r.enabled} onChange={(x) => patch(r.capability, 'enabled', x)} /> },
    { title: '', key: 'ops', width: 90, render: (r) => <div className="actions"><Button size="sm" onClick={() => saveRow(r)}>保存</Button></div> },
  ];

  return (
    <div className="stack-16">
      <Card title="默认模型" extra="对所有上游通用,改动即时生效">
        {!defaults.model && !loading && (
          <Notice tone="warn" style={{ marginBottom: 14 }}>
            还没设置默认模型,所有 AI 能力都无法调用。填一个上游支持的模型名并保存。
          </Notice>
        )}
        <div className="row-8">
          <Input mono value={defaults.model} onChange={(x) => setDefaults((d) => ({ ...d, model: x }))}
            placeholder="例如 gpt-4.1-mini" style={{ width: 320, maxWidth: '100%' }} />
          <Button variant="primary" loading={savingDef} onClick={saveDefaults}>保存</Button>
        </div>
        <p className="muted" style={{ marginTop: 10 }}>
          所有能力默认使用这个模型;下方某个能力单独填了模型才用它自己的。只影响之后的请求。
        </p>
      </Card>
      <Card flush title="能力列表">
        <Table columns={columns} rows={rows} rowKey="capability" loading={loading} />
      </Card>
    </div>
  );
}
