// ai-form-backend console - AGPL-3.0
import React, { useEffect, useState } from 'react';
import { Card, Table, Button, Toast, Input, InputNumber, Switch, Tag, Select } from '@douyinfe/semi-ui';
import { get, post, put } from '../../api.js';

// 套餐类型(计费方案 v1):base 底座给订阅资格;pack 只加积分且购买需有效底座;trial/bonus 系统发放
const PLAN_TYPES = [
  { value: 'base', label: '底座', color: 'blue' },
  { value: 'pack', label: '加油包', color: 'green' },
  { value: 'trial', label: '试用(自动发放)', color: 'grey' },
  { value: 'bonus', label: '赠送(首充礼)', color: 'orange' },
];
const typeMeta = (v) => PLAN_TYPES.find((t) => t.value === v) || { label: v || '未设置', color: 'red' };

export default function Plans() {
  const [rows, setRows] = useState([]);
  const [draft, setDraft] = useState({ name: '', planType: 'base', priceCents: 1990, totalCredits: 500, durationDays: 30 });

  const load = () => get('/v1/admin/plans').then((d) => {
    setRows(d.plans.map((p) => ({ ...p, key: p.ID })));
  }).catch((e) => Toast.error(e.message));

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
      Toast.success('已保存(只影响新订单)');
    } catch (e) { Toast.error(e.message); }
  };

  const create = async () => {
    try {
      await post('/v1/admin/plans', draft);
      Toast.success('已创建');
      load();
    } catch (e) { Toast.error(e.message); }
  };

  const columns = [
    { title: 'ID', dataIndex: 'ID', width: 60 },
    { title: '名称', dataIndex: 'Name', render: (v, r) => <Input value={v} onChange={(x) => patch(r.ID, 'Name', x)} style={{ width: 110 }} /> },
    { title: '价格(分)', dataIndex: 'PriceCents', render: (v, r) => <InputNumber value={v} onChange={(x) => patch(r.ID, 'PriceCents', x)} min={0} style={{ width: 110 }} /> },
    { title: '积分', dataIndex: 'TotalCredits', render: (v, r) => <InputNumber value={v} onChange={(x) => patch(r.ID, 'TotalCredits', x)} min={1} style={{ width: 110 }} /> },
    { title: '天数', dataIndex: 'DurationDays', render: (v, r) => <InputNumber value={v} onChange={(x) => patch(r.ID, 'DurationDays', x)} min={1} style={{ width: 90 }} /> },
    { title: '在售', dataIndex: 'ForSale', render: (v, r) => <Switch checked={v} onChange={(x) => patch(r.ID, 'ForSale', x)} /> },
    { title: '类型', dataIndex: 'PlanType', render: (v) => <Tag color={typeMeta(v).color}>{typeMeta(v).label}</Tag> },
    { title: '', render: (_, r) => <Button size="small" onClick={() => save(r)}>保存</Button> },
  ];

  return (
    <Card className="data-card" title="套餐列表" headerExtraContent="改价仅影响新订单，已发额度不受影响">
      <Table columns={columns} dataSource={rows} pagination={false} size="small" />
      <div className="create-toolbar">
        <Input placeholder="名称" value={draft.name} onChange={(v) => setDraft({ ...draft, name: v })} style={{ width: 120 }} />
        <Select value={draft.planType} onChange={(v) => setDraft({ ...draft, planType: v })} style={{ width: 150 }}
          optionList={PLAN_TYPES.map((t) => ({ value: t.value, label: t.label }))} />
        <InputNumber prefix="价格(分)" value={draft.priceCents} onChange={(v) => setDraft({ ...draft, priceCents: v })} min={0} style={{ width: 150 }} />
        <InputNumber prefix="积分" value={draft.totalCredits} onChange={(v) => setDraft({ ...draft, totalCredits: v })} min={1} style={{ width: 140 }} />
        <InputNumber prefix="天数" value={draft.durationDays} onChange={(v) => setDraft({ ...draft, durationDays: v })} min={1} style={{ width: 120 }} />
        <Button theme="solid" onClick={create} disabled={!draft.name}>新增套餐</Button>
      </div>
    </Card>
  );
}
