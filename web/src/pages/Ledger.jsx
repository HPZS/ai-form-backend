// ai-form-backend console - AGPL-3.0
// 积分流水:游标分页,只增不改的账目。
import React, { useEffect, useState } from 'react';
import { get } from '../api.js';
import { Card, Table, LoadMore, toast, fmtTime, fmtNum } from '../ui';

const capNames = {
  assess_page: '页面甄别', analyze_form: '表单识别', pick_open_button: '找开表单按钮',
  pick_form: '表单选型', match_columns: '建立录入方案', suggest_profile: '资料判型',
  detect_grouping: '层级判断', detect_identity: '身份字段判断',
  generate_rule: '生成规则', generate_field: 'AI 生成字段',
  explain_failure: '失败诊断', classify_failure: '失败分诊', parse_command: '指令解析',
  admin_grant: '管理员发放',
};

export default function Ledger() {
  const [rows, setRows] = useState([]);
  const [done, setDone] = useState(false);
  const [loading, setLoading] = useState(false);

  const load = async (beforeId) => {
    setLoading(true);
    try {
      const d = await get('/v1/credits/ledger' + (beforeId ? `?beforeId=${beforeId}` : ''));
      setRows((old) => [...old, ...d.entries]);
      if (d.entries.length < 50) setDone(true);
    } catch (e) { toast.error(e.message); }
    finally { setLoading(false); }
  };

  useEffect(() => { load(); }, []); // eslint-disable-line react-hooks/exhaustive-deps

  const columns = [
    { title: '时间', key: 'CreatedAt', width: 180, render: (r) => <span className="num muted">{fmtTime(r.CreatedAt)}</span> },
    { title: '事项', key: 'Capability', render: (r) => <span className="cell-title">{capNames[r.Capability] || r.Capability || '—'}</span> },
    { title: '单价', key: 'PriceSnapshot', align: 'right', render: (r) => <span className="num muted">{r.PriceSnapshot ?? '—'}</span> },
    {
      title: '变动', key: 'Delta', align: 'right',
      render: (r) => <span className={'num delta ' + (r.Delta < 0 ? 'neg' : 'pos')}>{r.Delta > 0 ? '+' : ''}{fmtNum(r.Delta)}</span>,
    },
    { title: '余额', key: 'BalanceAfter', align: 'right', render: (r) => <span className="num">{fmtNum(r.BalanceAfter)}</span> },
  ];

  return (
    <Card flush title="流水记录" extra={rows.length > 0 ? `已加载 ${rows.length} 条` : null}>
      <Table columns={columns} rows={rows} rowKey="ID" loading={loading} empty="还没有流水记录" />
      <LoadMore done={done} loading={loading} count={rows.length} onClick={() => load(rows[rows.length - 1].ID)} />
    </Card>
  );
}
