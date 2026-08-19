// ai-form-backend console - AGPL-3.0
// 弹层:Dialog(模态)、Confirm(气泡确认)、Menu(下拉菜单),基于 radix 无头组件。
import React, { useState } from 'react';
import { Dialog as RDialog, Popover, DropdownMenu } from 'radix-ui';
import { X } from 'lucide-react';
import { Button } from './Button.jsx';

export function Dialog({ open, onClose, title, width, children }) {
  return (
    <RDialog.Root open={open} onOpenChange={(o) => { if (!o) onClose(); }}>
      <RDialog.Portal>
        <RDialog.Overlay className="overlay" />
        <RDialog.Content className="dialog" style={width ? { '--dialog-w': width + 'px' } : undefined}>
          <div className="dialog-head">
            <RDialog.Title className="dialog-title">{title}</RDialog.Title>
            <RDialog.Close asChild><Button variant="ghost" size="sm" icon={<X />} aria-label="关闭" /></RDialog.Close>
          </div>
          <div className="dialog-body">{children}</div>
        </RDialog.Content>
      </RDialog.Portal>
    </RDialog.Root>
  );
}

/** 危险操作二次确认:点击 children 弹出气泡,确认后执行 onConfirm */
export function Confirm({ title, onConfirm, okText = '确认', children }) {
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const ok = async () => {
    setBusy(true);
    try { await onConfirm(); setOpen(false); } finally { setBusy(false); }
  };
  return (
    <Popover.Root open={open} onOpenChange={setOpen}>
      <Popover.Trigger asChild>{children}</Popover.Trigger>
      <Popover.Portal>
        <Popover.Content className="popover" align="end" sideOffset={6} collisionPadding={12}>
          <div className="popover-title">{title}</div>
          <div className="popover-actions">
            <Button size="sm" variant="ghost" onClick={() => setOpen(false)}>取消</Button>
            <Button size="sm" variant="danger-solid" loading={busy} onClick={ok}>{okText}</Button>
          </div>
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}

/** items: [{ label, icon, danger, onSelect }] 或 'sep' 或 { header } */
export function Menu({ items, align = 'start', side, children }) {
  return (
    <DropdownMenu.Root>
      <DropdownMenu.Trigger asChild>{children}</DropdownMenu.Trigger>
      <DropdownMenu.Portal>
        <DropdownMenu.Content className="menu" align={align} side={side} sideOffset={6} collisionPadding={12}
          style={{ minWidth: 'var(--radix-dropdown-menu-trigger-width)' }}>
          {items.map((it, i) => {
            if (it === 'sep') return <DropdownMenu.Separator key={i} className="menu-sep" />;
            if (it.header) return <DropdownMenu.Label key={i} className="menu-label">{it.header}</DropdownMenu.Label>;
            return (
              <DropdownMenu.Item key={i} className={'menu-item' + (it.danger ? ' danger' : '')} onSelect={it.onSelect}>
                {it.icon}{it.label}
              </DropdownMenu.Item>
            );
          })}
        </DropdownMenu.Content>
      </DropdownMenu.Portal>
    </DropdownMenu.Root>
  );
}
