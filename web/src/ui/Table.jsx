// ai-form-backend console - AGPL-3.0
// 轻量表格:columns = [{ title, key, render(row), width, align }]
import React from 'react';
import { Inbox, ChevronLeft, ChevronRight } from 'lucide-react';
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

/** 页码分页:总数 + 上一页/页码/下一页。页码最多显示 5 个,两端用 … 省略 */
export function Pagination({ page, pageSize, total, onChange, loading }) {
  const pages = Math.max(1, Math.ceil(total / pageSize));
  if (total === 0) return null;
  const nums = [];
  const lo = Math.max(1, Math.min(page - 2, pages - 4));
  const hi = Math.min(pages, lo + 4);
  for (let i = lo; i <= hi; i++) nums.push(i);
  return (
    <div className="pagination">
      <span className="pagination-info num">共 {total.toLocaleString('zh-CN')} 条 · 第 {page} / {pages} 页</span>
      <div className="pagination-pages">
        <Button variant="ghost" size="sm" icon={<ChevronLeft />} aria-label="上一页" disabled={page <= 1 || loading} onClick={() => onChange(page - 1)} />
        {lo > 1 && <><button type="button" className="page-btn" onClick={() => onChange(1)}>1</button><span className="page-gap">…</span></>}
        {nums.map((n) => (
          <button key={n} type="button" className={'page-btn num' + (n === page ? ' on' : '')} disabled={loading} onClick={() => onChange(n)}>{n}</button>
        ))}
        {hi < pages && <><span className="page-gap">…</span><button type="button" className="page-btn" onClick={() => onChange(pages)}>{pages}</button></>}
        <Button variant="ghost" size="sm" icon={<ChevronRight />} aria-label="下一页" disabled={page >= pages || loading} onClick={() => onChange(page + 1)} />
      </div>
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
