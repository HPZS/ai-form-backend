// ai-form-backend console - AGPL-3.0
// 积分流水:游标分页,只增不改的账目。
import React, { useEffect, useState } from 'react';
import { Card, Table, Button, Toast, Tag } from '@douyinfe/semi-ui';
import { get } from '../api.js';

const capNames = {
  assess_page: '页面甄别', analyze_form: '表单识别', pick_open_button: '找开表单按钮',
  pick_form: '表单选型', match_columns: '建立映射', suggest_profile: '资料判型',
  detect_grouping: '层级判断', generate_rule: '生成规则', generate_field: '生成字段',
  explain_failure: '失败诊断', classify_failure: '失败分诊', parse_command: '指令解析',
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
    } catch (e) { Toast.error(e.message); }
    finally { setLoading(false); }
  };

  useEffect(() => { load(); }, []); // eslint-disable-line react-hooks/exhaustive-deps

  const columns = [
    { title: '时间', dataIndex: 'CreatedAt', render: (v) => new Date(v).toLocaleString('zh-CN') },
    { title: '能力', dataIndex: 'Capability', render: (v) => capNames[v] || v || '-' },
    {
      title: '积分变动', dataIndex: 'Delta',
      render: (v) => <Tag color={v < 0 ? 'orange' : 'green'}>{v > 0 ? '+' : ''}{v}</Tag>,
    },
    { title: '单价快照', dataIndex: 'PriceSnapshot' },
    { title: '变动后可用', dataIndex: 'BalanceAfter' },
  ];

  return (
    <Card title="积分流水">
      <Table columns={columns} dataSource={rows} pagination={false} rowKey="ID" size="small" loading={loading && rows.length === 0} />
      <div style={{ textAlign: 'center', marginTop: 12 }}>
        {!done && rows.length > 0 && (
          <Button loading={loading} onClick={() => load(rows[rows.length - 1].ID)}>加载更多</Button>
        )}
      </div>
    </Card>
  );
}
