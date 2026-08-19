// ai-form-backend console - AGPL-3.0
import React from 'react';

export function Spinner({ size }) {
  return <span className={'spinner' + (size === 'lg' ? ' lg' : '')} aria-hidden="true" />;
}

/**
 * variant: 'default' | 'primary' | 'ghost' | 'danger' | 'danger-solid'
 * size: 'sm' | 'md' | 'lg'
 */
export const Button = React.forwardRef(function Button(
  { variant = 'default', size = 'md', block, loading, icon, className = '', children, type = 'button', disabled, ...rest }, ref,
) {
  const cls = ['btn',
    variant === 'danger-solid' ? 'danger solid' : variant !== 'default' ? variant : '',
    size !== 'md' ? size : '',
    block ? 'block' : '',
    loading ? 'loading' : '',
    icon && !children ? 'icon' : '',
    className,
  ].filter(Boolean).join(' ');
  return (
    <button ref={ref} type={type} className={cls} disabled={disabled || loading} {...rest}>
      {loading && <Spinner />}
      {icon}
      {children}
    </button>
  );
});
