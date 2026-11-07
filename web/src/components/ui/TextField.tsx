import type { InputHTMLAttributes } from 'react';

type Props = InputHTMLAttributes<HTMLInputElement> & {
  id: string;
  label: string;
  hint?: string;
  error?: string;
  optional?: boolean;
};

export function TextField({ id, label, hint, error, optional, className = '', ...props }: Props) {
  const hintId = hint ? `${id}-hint` : undefined;
  const errorId = error ? `${id}-error` : undefined;
  const describedBy = [props['aria-describedby'], hintId, errorId].filter(Boolean).join(' ') || undefined;
  return (
    <div className="ui-field">
      <label className="ui-label" htmlFor={id}>{label}{optional&&<> <span className="ui-label__optional">optional</span></>}</label>
      <input {...props} id={id} aria-invalid={error ? true : props['aria-invalid']} aria-describedby={describedBy} className={`ui-control ${error ? 'ui-control--invalid' : ''} ${className}`.trim()} />
      {hint && <p className="ui-hint" id={hintId}>{hint}</p>}
      {error && <p className="ui-error" id={errorId}>{error}</p>}
    </div>
  );
}
