// ai-form-backend console - AGPL-3.0
import React from 'react';
import { TriangleAlert, Info } from 'lucide-react';

/** tone: 'neutral' | 'accent' | 'ok' | 'warn' | 'danger';dot 在前面加状态点 */
export function Tag({ tone = 'neutral', dot, children }) {
  return <span className={['tag', tone !== 'neutral' ? tone : '', dot ? 'dot' : ''].filter(Boolean).join(' ')}>{children}</span>;
}

export function Notice({ tone = 'info', children, style }) {
  return (
    <div className={'notice ' + tone} style={style}>
      {tone === 'warn' ? <TriangleAlert /> : <Info />}
      <div>{children}</div>
    </div>
  );
}

/** value 0..1;低于 lowAt 用警示色 */
export function Progress({ value, lowAt = 0.15 }) {
  const v = Math.max(0, Math.min(1, value || 0));
  return <div className={'progress' + (v < lowAt ? ' low' : '')}><i style={{ width: `${v * 100}%` }} /></div>;
}

export const fmtTime = (v) => new Date(v).toLocaleString('zh-CN', { hour12: false });
export const fmtDate = (v) => new Date(v).toLocaleDateString('zh-CN');
export const fmtNum = (n) => (n ?? 0).toLocaleString('zh-CN');
