// ai-form-backend console - AGPL-3.0
import React from 'react';

/** flush: 内容区不留内边距(放表格);foot: 卡片底部工具条;note: 底部说明 */
export function Card({ title, extra, flush, foot, note, className = '', style, children }) {
  return (
    <section className={['card', flush ? 'flush' : '', className].filter(Boolean).join(' ')} style={style}>
      {(title || extra) && (
        <header className="card-head">
          <h3 className="card-title">{title}</h3>
          {extra && <div className="card-extra">{extra}</div>}
        </header>
      )}
      <div className="card-body">{children}</div>
      {foot && <div className="card-foot">{foot}</div>}
      {note && <div className="card-note">{note}</div>}
    </section>
  );
}
