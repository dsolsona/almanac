import type { ReactNode, Ref } from 'react';
import { StatusMark, type MarkTone } from './StatusMark.tsx';
import './operational-controls.css';

type Props = {
  tone: MarkTone;
  status: string;
  title: ReactNode;
  badge?: string;
  detail?: ReactNode;
  message?: ReactNode;
  action?: { label: string; onClick: () => void; ref?: Ref<HTMLButtonElement> };
  variant?: 'default' | 'warning';
  className?: string;
};

// The collapsed state of a picker: what was chosen, its state, and a way to change it.
export function SelectionSummary({ tone, status, title, badge, detail, message, action, variant = 'default', className = '' }: Props) {
  return <div className={`ui-summary ui-summary--${variant} ${className}`.trim()}>
    <StatusMark tone={tone} label={status} />
    <span className="ui-summary__title">{title}{badge && <span className="ui-pill">{badge}</span>}</span>
    {action && <button ref={action.ref} type="button" className="ui-summary__action" onClick={action.onClick}>{action.label}</button>}
    {detail && <span className="ui-summary__detail">{detail}</span>}
    {message && <p className="ui-summary__message">{message}</p>}
  </div>;
}
