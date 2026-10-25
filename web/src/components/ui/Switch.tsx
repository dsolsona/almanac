import { useId, type InputHTMLAttributes } from 'react';

type Props = Omit<InputHTMLAttributes<HTMLInputElement>, 'type' | 'role'> & {
  label: string;
  hint?: string;
};

export function Switch({ label, hint, id, className = '', ...props }: Props) {
  const generatedId = useId();
  const controlId = id ?? `switch-${generatedId}`;
  const hintId = hint ? `${controlId}-hint` : undefined;
  return <div className={`ui-switch ${className}`.trim()}>
    <label className="ui-switch__label" htmlFor={controlId}>
      <span className="ui-switch__copy"><span>{label}</span></span>
      <span className="ui-switch__state" aria-hidden="true"><span className="ui-switch__state--on">On</span><span className="ui-switch__state--off">Off</span></span>
      <span className="ui-switch__track" aria-hidden="true"><span /></span>
    </label>
    {hint && <span className="ui-switch__hint" id={hintId}>{hint}</span>}
    <input {...props} id={controlId} type="checkbox" role="switch" aria-describedby={[props['aria-describedby'], hintId].filter(Boolean).join(' ') || undefined} className="ui-switch__input" />
  </div>;
}
