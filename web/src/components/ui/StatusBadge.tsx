export type Status = 'healthy' | 'warning' | 'breached' | 'no-data' | 'stale';

const labels: Record<Status, string> = {
  healthy: 'Healthy', warning: 'Warning', breached: 'Breached', 'no-data': 'No data', stale: 'Stale',
};
const marks: Record<Status, string> = { healthy: '✓', warning: '!', breached: '×', 'no-data': '–', stale: '↻' };

export function StatusBadge({ status }: { status: Status }) {
  return <span className={`ui-status ui-status--${status}`}><span className="ui-status__mark" aria-hidden="true">{marks[status]}</span>{labels[status]}</span>;
}
