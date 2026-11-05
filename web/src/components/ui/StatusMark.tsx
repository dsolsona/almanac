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
// Health in a table cell (ADR-0031): the same shape vocabulary inside a tinted pill; the reason
// line appears only when the state needs attention, so healthy rows stay quiet.
export function StatusChip({ tone, label, reason, className = '' }: Props) {
  return <span className={`ui-status-chip-cell ${className}`.trim()}>
    <span className={`ui-status-chip ui-status-chip--${tone}`}><span className="ui-status-chip__shape" aria-hidden="true" />{label}</span>
    {reason && tone !== 'healthy' && <span className="ui-status-chip__reason">{reason}</span>}
  </span>;
}

export function StatusMark({ tone, label, reason, className = '' }: Props) {
  return <span className={`ui-mark ui-mark--${tone} ${className}`.trim()}>
    <span className="ui-mark__row"><span className="ui-mark__shape" aria-hidden="true" />{label}</span>
    {reason && <span className="ui-mark__reason">{reason}</span>}
  </span>;
}
