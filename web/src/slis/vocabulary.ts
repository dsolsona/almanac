import type { MarkTone } from '../components/ui/StatusMark.tsx';
import type { Sli } from './client.ts';

export const clockFormat = new Intl.DateTimeFormat('en-GB', { hour: '2-digit', minute: '2-digit', second: '2-digit', timeZone: 'UTC', hour12: false });

export function clockTime(value: string): string {
  return `${clockFormat.format(new Date(value))} UTC`;
}

export function relativeAge(value: string, now = Date.now()): string {
  const seconds = Math.max(0, Math.round((now - Date.parse(value)) / 1000));
  if (seconds < 90) return `${seconds} s ago`;
  if (seconds < 5400) return `${Math.round(seconds / 60)} min ago`;
  if (seconds < 172800) return `${Math.round(seconds / 3600)} h ago`;
  return `${Math.round(seconds / 86400)} d ago`;
}

export type SliVocabulary = { tone: MarkTone; label: string; reason: string; attention: boolean };

// ADR-0028 vocabulary for SLIs, from the list projection fields only.
export function sliVocabulary(item: Sli): SliVocabulary {
  const latest = item.latest_observation;
  const agent = item.agent_reachability.replaceAll('_', ' ');
  if (item.freshness === 'stale') return { tone: 'stale', label: 'Not reporting', reason: item.agent_reachability !== 'connected' ? `agent ${agent}` : item.last_observation_at ? `last sample ${relativeAge(item.last_observation_at)}` : 'no recent sample', attention: true };
  if (!latest || item.freshness === 'unknown') return { tone: 'none', label: 'Waiting', reason: item.activation_state === 'blocked' || item.collection_progress === 'blocked' ? 'collection blocked' : item.next_expected_execution_at ? `first query ${clockTime(item.next_expected_execution_at)}` : 'first interval pending', attention: item.activation_state === 'blocked' || item.collection_progress === 'blocked' };
  if (latest.outcome === 'complete_empty' || latest.total_count === '0') return { tone: 'none', label: 'Zero traffic', reason: 'queries return 0 events', attention: false };
  if (latest.outcome === 'failed') return { tone: 'warning', label: 'Failing', reason: `last query ${latest.error_code ?? 'failed'}`, attention: true };
  if (latest.outcome === 'incomplete') return { tone: 'warning', label: 'Failing', reason: `missing ${latest.missing_components.join(', ')}`, attention: true };
  return { tone: 'healthy', label: 'Healthy', reason: item.agent_reachability === 'connected' ? 'agent connected' : `agent ${agent}`, attention: false };
}
