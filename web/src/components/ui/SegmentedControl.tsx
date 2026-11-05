import type { ReactNode } from 'react';
import { moveRadioFocus } from './radioGroup.ts';
import './operational-controls.css';

export type SegmentOption<T extends string = string> = {
  value: T;
  label: ReactNode;
  count?: number;
  disabled?: boolean;
  title?: string;
};

type Props<T extends string> = {
  label: string;
  options: SegmentOption<T>[];
  value: T;
  onChange: (value: T) => void;
  size?: 'md' | 'sm';
  className?: string;
};

// Single choice among a few fixed options. Native button semantics with radiogroup roles,
// roving tabindex and arrow keys; disabled options keep a title explaining why.
export function SegmentedControl<T extends string>({ label, options, value, onChange, size = 'md', className = '' }: Props<T>) {
  const firstEnabled = options.find((option) => !option.disabled)?.value;
  const focusable = options.some((option) => option.value === value && !option.disabled) ? value : firstEnabled;
  return <div role="radiogroup" aria-label={label} className={`ui-segmented ui-segmented--${size} ${className}`.trim()} onKeyDown={(event) => moveRadioFocus(event, (next) => onChange(next as T))}>
    {options.map((option) => <button
      key={option.value}
      type="button"
      role="radio"
      value={option.value}
      aria-checked={option.value === value}
      tabIndex={option.value === focusable ? 0 : -1}
      disabled={option.disabled}
      title={option.title}
      className="ui-segmented__option"
      onClick={() => onChange(option.value)}
    >{option.label}{option.count !== undefined && <> <span className="ui-segmented__count">{option.count}</span></>}</button>)}
  </div>;
}
