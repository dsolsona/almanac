import type { SelectHTMLAttributes } from 'react';

export type SelectOption = { value: string; label: string; disabled?: boolean };
type Props = SelectHTMLAttributes<HTMLSelectElement> & {
  id: string;
  label: string;
  options: SelectOption[];
  hint?: string;
  error?: string;
};

export function SelectField({ id, label, options, hint, error, className = '', ...props }: Props) {
  const hintId = hint ? `${id}-hint` : undefined;
  const errorId = error ? `${id}-error` : undefined;
  const describedBy = [props['aria-describedby'], hintId, errorId].filter(Boolean).join(' ') || undefined;
  return (
    <div className="ui-field">
      <label className="ui-label" htmlFor={id}>{label}</label>
      <select {...props} id={id} aria-invalid={error ? true : props['aria-invalid']} aria-describedby={describedBy} className={`ui-control ${error ? 'ui-control--invalid' : ''} ${className}`.trim()}>
        {options.map((option) => <option key={option.value} value={option.value} disabled={option.disabled}>{option.label}</option>)}
      </select>
      {hint && <p className="ui-hint" id={hintId}>{hint}</p>}
      {error && <p className="ui-error" id={errorId}>{error}</p>}
    </div>
  );
}
