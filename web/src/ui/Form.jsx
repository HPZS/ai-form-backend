// ai-form-backend console - AGPL-3.0
// 表单控件:受控、onChange 直接回传值(不是事件),保持各页写法简短。
import React from 'react';
import { Switch as RSwitch } from 'radix-ui';
import { ChevronDown } from 'lucide-react';

function wrapCls(base, { size, mono, className }) {
  return [base, size && size !== 'md' ? size : '', mono ? 'mono' : '', className || ''].filter(Boolean).join(' ');
}

export function Input({ value, onChange, prefix, suffix, size, mono, className, style, ...rest }) {
  return (
    <label className={wrapCls('input', { size, mono, className })} style={style}>
      {prefix && <span className="input-prefix">{prefix}</span>}
      <input value={value ?? ''} onChange={(e) => onChange?.(e.target.value)} {...rest} />
      {suffix && <span className="input-suffix">{suffix}</span>}
    </label>
  );
}

/** 空输入回传 null,由调用方决定如何处理 */
export function NumberInput({ value, onChange, ...rest }) {
  return (
    <Input
      type="number" inputMode="numeric"
      value={value ?? ''}
      onChange={(v) => onChange?.(v === '' ? null : Number(v))}
      {...rest}
    />
  );
}

export function Select({ value, onChange, options, size, className, style, ...rest }) {
  return (
    <label className={wrapCls('input select', { size, className })} style={style}>
      <select value={value} onChange={(e) => onChange?.(e.target.value)} {...rest}>
        {options.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
      </select>
      <ChevronDown />
    </label>
  );
}

export function Field({ label, hint, children }) {
  return (
    <div className="field">
      {label && <span className="field-label">{label}</span>}
      {children}
      {hint && <span className="field-hint">{hint}</span>}
    </div>
  );
}

export function Switch({ checked, onChange, ...rest }) {
  return (
    <RSwitch.Root className="switch" checked={!!checked} onCheckedChange={onChange} {...rest}>
      <RSwitch.Thumb className="switch-thumb" />
    </RSwitch.Root>
  );
}

export function Segmented({ value, onChange, options, block }) {
  return (
    <div className={'segmented' + (block ? ' block' : '')} role="tablist">
      {options.map((o) => (
        <button key={o.value} type="button" role="tab" aria-selected={value === o.value}
          className={value === o.value ? 'on' : ''} onClick={() => onChange(o.value)}>
          {o.label}
        </button>
      ))}
    </div>
  );
}
