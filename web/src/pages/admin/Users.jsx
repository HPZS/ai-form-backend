// ai-form-backend console - AGPL-3.0
import React, { useEffect, useState } from 'react';
import { Card, Table, Button, Toast, Tag, Popconfirm } from '@douyinfe/semi-ui';
import { get, put } from '../../api.js';

export default function Users() {
  const [rows, setRows] = useState([]);
  const [done, setDone] = useState(false);
  const [loading, setLoading] = useState(false);

  const load = async (beforeId) => {
    setLoading(true);
    try {
      const d = await get('/v1/admin/users' + (beforeId ? `?beforeId=${beforeId}` : ''));
      setRows((old) => (beforeId ? [...old, ...d.users] : d.users));
      if (d.users.length < 100) setDone(true);
    } catch (e) { Toast.error(e.message); }
    finally { setLoading(false); }
  };

  useEffect(() => { load(); }, []); // eslint-disable-line react-hooks/exhaustive-deps

  const setStatus = async (u, status) => {
    try {
      await put('/v1/admin/users/' + u.ID, { status });
      Toast.success(status === 'banned' ? '已封禁(全端登出)' : '已解封');
      setRows((old) => old.map((r) => (r.ID === u.ID ? { ...r, Status: status } : r)));
    } catch (e) { Toast.error(e.message); }
  };

  const columns = [
    { title: 'ID', dataIndex: 'ID', width: 70 },
    { title: '邮箱', dataIndex: 'Email' },
    { title: '角色', dataIndex: 'Role', render: (v) => (v === 'admin' ? <Tag color="blue">管理员</Tag> : '用户') },
    { title: '状态', dataIndex: 'Status', render: (v) => (v === 'active' ? <Tag color="green">正常</Tag> : <Tag color="red">封禁</Tag>) },
    { title: '注册时间', dataIndex: 'CreatedAt', render: (v) => new Date(v).toLocaleString('zh-CN') },
    {
      title: '', render: (_, u) => (
        u.Role === 'admin' ? null : u.Status === 'active' ? (
          <Popconfirm title={`封禁 ${u.Email}?将立即全端登出。`} onConfirm={() => setStatus(u, 'banned')}>
            <Button size="small" type="danger">封禁</Button>
          </Popconfirm>
        ) : (
          <Button size="small" onClick={() => setStatus(u, 'active')}>解封</Button>
        )
      ),
    },
  ];

  return (
    <Card className="data-card" title="用户列表">
      <Table columns={columns} dataSource={rows} pagination={false} size="small" rowKey="ID" loading={loading && rows.length === 0} />
      <div className="load-more-row">
        {!done && rows.length > 0 && (
          <Button loading={loading} onClick={() => load(rows[rows.length - 1].ID)}>加载更多</Button>
        )}
      </div>
    </Card>
  );
}
