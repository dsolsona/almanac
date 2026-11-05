import './operational-controls.css';

export type MarkTone = 'healthy' | 'warning' | 'breached' | 'stale' | 'none';

type Props = {
  tone: MarkTone;
  label: string;
  reason?: string;
  className?: string;
};

// Shape plus label plus reason. The shape carries the tone for colour-blind readers:
// filled circle healthy, triangle warning, square breached, open circle stale, dashed none.
export function StatusMark({ tone, label, reason, className = '' }: Props) {
  return <span className={`ui-mark ui-mark--${tone} ${className}`.trim()}>
    <span className="ui-mark__row"><span className="ui-mark__shape" aria-hidden="true" />{label}</span>
    {reason && <span className="ui-mark__reason">{reason}</span>}
  </span>;
}
