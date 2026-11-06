import './operational-controls.css';

export const roleLabels: Record<string,string> = { viewer:'Viewer', editor:'Editor', administrator:'Admin' };

export function RoleChip({ role }: { role:string }) {
  return <span className="ui-status-chip ui-role-chip" aria-label={`Role: ${roleLabels[role] ?? role}`}>{roleLabels[role] ?? role}</span>;
}
