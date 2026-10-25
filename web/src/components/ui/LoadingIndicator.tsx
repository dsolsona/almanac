type Props = { label: string; className?: string };

export function LoadingIndicator({ label, className = '' }: Props) {
  return <span className={`ui-loading ${className}`.trim()} role="status" aria-label={label}>
    <span className="ui-loading__track" aria-hidden="true"><span className="ui-loading__segment" /></span>
    <span className="ui-loading__text">{label}</span>
  </span>;
}
