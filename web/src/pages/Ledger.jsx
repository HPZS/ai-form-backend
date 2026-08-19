// ai-form-backend console - AGPL-3.0
// 积分流水:页码分页,只增不改的账目。
import React, { useEffect, useState } from 'react';
import { get } from '../api.js';
import { Card, Table, Pagination, toast, fmtTime, fmtNum } from '../ui';

const PAGE_SIZE = 20;

const capNames = {
  assess_page: '页面甄别', analyze_form: '表单识别', pick_open_button: '找开表单按钮',
  pick_form: '表单选型', match_columns: '建立录入方案', suggest_profile: '资料判型',
  detect_grouping: '层级判断', detect_identity: '身份字段判断',
  generate_rule: '生成规则', generate_field: 'AI 生成字段',
  explain_failure: '失败诊断', classify_failure: '失败分诊', parse_command: '指令解析',
  admin_grant: '管理员发放',
};

export default function Ledger() {
  const [page, setPage] = useState(1);
  const [data, setData] = useState({ entries: [], total: 0 });
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let stale = false;
    setLoading(true);
    get(`/v1/credits/ledger?page=${page}&pageSize=${PAGE_SIZE}`)
      .then((d) => { if (!stale) setData(d); })
      .catch((e) => toast.error(e.message))
      .finally(() => { if (!stale) setLoading(false); });
    return () => { stale = true; };
  }, [page]);

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
    <Card flush title="流水记录">
      <Table columns={columns} rows={data.entries} rowKey="ID" loading={loading} empty="还没有流水记录" />
      <Pagination page={page} pageSize={PAGE_SIZE} total={data.total} loading={loading} onChange={setPage} />
    </Card>
  );
}
