// ai-form-backend console - AGPL-3.0
import React, { useEffect, useState } from 'react';
import { Plus } from 'lucide-react';
import { get, post, put } from '../../api.js';
import { Card, Table, Button, Input, NumberInput, Select, Switch, Tag, toast } from '../../ui';

// 套餐类型(计费方案 v1):base 底座给订阅资格;pack 只加积分且购买需有效底座;trial/bonus 系统发放
const PLAN_TYPES = [
  { value: 'base', label: '底座', tone: 'accent' },
  { value: 'pack', label: '加油包', tone: 'ok' },
  { value: 'trial', label: '试用(自动发放)', tone: 'neutral' },
  { value: 'bonus', label: '赠送(首充礼)', tone: 'warn' },
];
const typeMeta = (v) => PLAN_TYPES.find((t) => t.value === v) || { label: v || '未设置', tone: 'danger' };

export default function Plans() {
  const [rows, setRows] = useState([]);
  const [loading, setLoading] = useState(true);
  const [draft, setDraft] = useState({ name: '', planType: 'base', priceCents: 1990, totalCredits: 500, durationDays: 30 });
  const [creating, setCreating] = useState(false);

  const load = () => get('/v1/admin/plans').then((d) => setRows(d.plans))
    .catch((e) => toast.error(e.message)).finally(() => setLoading(false));

  useEffect(() => { load(); }, []);

  const patch = (id, field, value) => {
    setRows((old) => old.map((r) => (r.ID === id ? { ...r, [field]: value } : r)));
  };

  const save = async (r) => {
    try {
      await put('/v1/admin/plans/' + r.ID, {
        name: r.Name, planType: r.PlanType, priceCents: r.PriceCents, totalCredits: r.TotalCredits,
        durationDays: r.DurationDays, forSale: r.ForSale,
      });
      toast.success('已保存(只影响新订单)');
    } catch (e) { toast.error(e.message); }
  };

  const create = async () => {
    setCreating(true);
    try {
      await post('/v1/admin/plans', draft);
      toast.success('已创建');
      load();
    } catch (e) { toast.error(e.message); }
    finally { setCreating(false); }
  };

  const columns = [
    { title: 'ID', key: 'ID', width: 60, render: (r) => <span className="mono muted">{r.ID}</span> },
    { title: '名称', key: 'Name', render: (r) => <Input size="sm" value={r.Name} onChange={(x) => patch(r.ID, 'Name', x)} style={{ width: 130 }} /> },
    { title: '类型', key: 'PlanType', render: (r) => <Tag tone={typeMeta(r.PlanType).tone}>{typeMeta(r.PlanType).label}</Tag> },
    { title: '价格(分)', key: 'PriceCents', render: (r) => <NumberInput size="sm" value={r.PriceCents} onChange={(x) => patch(r.ID, 'PriceCents', x ?? 0)} min={0} style={{ width: 100 }} /> },
    { title: '积分', key: 'TotalCredits', render: (r) => <NumberInput size="sm" value={r.TotalCredits} onChange={(x) => patch(r.ID, 'TotalCredits', x ?? 0)} min={1} style={{ width: 100 }} /> },
    { title: '天数', key: 'DurationDays', render: (r) => <NumberInput size="sm" value={r.DurationDays} onChange={(x) => patch(r.ID, 'DurationDays', x ?? 0)} min={1} style={{ width: 80 }} /> },
    { title: '在售', key: 'ForSale', render: (r) => <Switch checked={r.ForSale} onChange={(x) => patch(r.ID, 'ForSale', x)} /> },
    { title: '', key: 'ops', render: (r) => <div className="actions"><Button size="sm" onClick={() => save(r)}>保存</Button></div> },
  ];

  return (
    <Card flush title="套餐列表" extra="改价仅影响新订单,已发额度不受影响"
      foot={
        <>
          <Input size="sm" placeholder="名称" value={draft.name} onChange={(v) => setDraft({ ...draft, name: v })} style={{ width: 130 }} />
          <Select size="sm" value={draft.planType} onChange={(v) => setDraft({ ...draft, planType: v })} style={{ width: 150 }}
            options={PLAN_TYPES.map((t) => ({ value: t.value, label: t.label }))} />
          <NumberInput size="sm" prefix="价格(分)" value={draft.priceCents} onChange={(v) => setDraft({ ...draft, priceCents: v ?? 0 })} min={0} style={{ width: 140 }} />
          <NumberInput size="sm" prefix="积分" value={draft.totalCredits} onChange={(v) => setDraft({ ...draft, totalCredits: v ?? 0 })} min={1} style={{ width: 120 }} />
          <NumberInput size="sm" prefix="天数" value={draft.durationDays} onChange={(v) => setDraft({ ...draft, durationDays: v ?? 0 })} min={1} style={{ width: 110 }} />
          <Button size="sm" variant="primary" icon={<Plus />} loading={creating} onClick={create} disabled={!draft.name}>新增套餐</Button>
        </>
      }>
      <Table columns={columns} rows={rows} rowKey="ID" loading={loading} />
    </Card>
  );
}
