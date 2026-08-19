// ai-form-backend console - AGPL-3.0
// 轻量表格:columns = [{ title, key, render(row), width, align }]
import React from 'react';
import { Inbox } from 'lucide-react';
import { Spinner, Button } from './Button.jsx';

export function Table({ columns, rows, rowKey, empty = '暂无数据', loading }) {
  const keyOf = typeof rowKey === 'function' ? rowKey : (r) => r[rowKey];
  return (
    <div className="table-wrap">
      <table className="table">
        <thead>
          <tr>
            {columns.map((c, i) => (
              <th key={c.key || i} style={{ width: c.width }} className={c.align === 'right' ? 'right' : ''}>{c.title}</th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => (
            <tr key={keyOf(r)}>
              {columns.map((c, i) => (
                <td key={c.key || i} className={c.align === 'right' ? 'right' : ''}>
                  {c.render ? c.render(r) : r[c.key]}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
      {loading && rows.length === 0 && <div className="table-loading"><Spinner size="lg" /></div>}
      {!loading && rows.length === 0 && (
        <div className="empty"><Inbox /><span>{empty}</span></div>
      )}
    </div>
  );
}

/** 游标分页的"加载更多"行 */
export function LoadMore({ done, loading, onClick, count }) {
  if (done || count === 0) return null;
  return (
    <div className="load-more">
      <Button variant="ghost" size="sm" loading={loading} onClick={onClick}>加载更多</Button>
    </div>
  );
}
