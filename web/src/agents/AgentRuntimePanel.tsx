import type { Agent } from './client.ts';
import { Panel } from '../components/ui/index.ts';

export function AgentRuntimePanel({ agent }: { agent: Agent }) {
  const runtime = agent.runtime ?? null;
  const connection = !agent.enabled ? 'Disabled by administrator'
    : runtime === null ? 'Never connected'
      : runtime.reachability === 'connected' ? 'Connected'
        : runtime.reachability === 'unreachable' ? 'Unreachable' : 'Incompatible';

  return <Panel heading="Agent runtime" className="agents-panel agents-runtime">
    <p className="agents-runtime__state"><strong>Connection state:</strong> {connection}</p>
    {runtime === null ? <p>No heartbeat has been received from this agent.</p> : <>
      {!agent.enabled && <p className="agents-runtime__disabled">The agent is not operational while disabled, even if its latest report shows a connection.</p>}
      <dl className="agents-runtime__meta">
        <div><dt>Reported reachability</dt><dd>{runtime.reachability}{!agent.enabled ? ' (not operational while disabled)' : ''}</dd></div>
        <div><dt>Protocol</dt><dd>{runtime.protocol}</dd></div>
        <div><dt>Software version</dt><dd>{runtime.software_version}</dd></div>
        <div><dt>Last heartbeat</dt><dd>{reportedDate(runtime.last_heartbeat_at)}</dd></div>
        <div><dt>Desired configuration revision</dt><dd>{runtime.desired_configuration_revision}</dd></div>
        <div><dt>Applied configuration revision</dt><dd>{runtime.applied_configuration_revision === '0' ? 'Not applied' : runtime.applied_configuration_revision}</dd></div>
        <div><dt>Scheduler health</dt><dd>{runtime.scheduler_health}</dd></div>
        <div><dt>Collection progress</dt><dd>{runtime.collection_progress}</dd></div>
        {runtime.configuration_failure_code !== null && <div><dt>Configuration failure</dt><dd>{runtime.configuration_failure_code} (revision {runtime.configuration_failure_revision})</dd></div>}
        <div><dt>Last collection attempt</dt><dd>{reportedDate(runtime.last_attempt_at)}</dd></div>
        <div><dt>Last successful query</dt><dd>{runtime.last_successful_query_at === null ? 'No successful query has been reported.' : reportedDate(runtime.last_successful_query_at)}</dd></div>
        <div><dt>Next expected execution</dt><dd>{reportedDate(runtime.next_expected_execution_at)}</dd></div>
        {runtime.collection_explanation === 'no_assignment' && <div><dt>Collection explanation</dt><dd>No datasources are assigned to this agent.</dd></div>}
      </dl>
      <section className="agents-runtime__capabilities" aria-labelledby="agents-runtime-capabilities">
        <h3 id="agents-runtime-capabilities">Provider capabilities</h3>
        {runtime.capabilities.length === 0 ? <p>No provider capabilities were reported.</p> : <ul>{runtime.capabilities.map((capability, index) => <li key={`${capability.provider}:${index}`}>
          <strong>{capability.provider}</strong>
          <span>Configuration formats: {capability.configuration_formats.length ? capability.configuration_formats.join(', ') : 'None reported'}</span>
          <span>Operations: {capability.operations.length ? capability.operations.join(', ') : 'None reported'}</span>
        </li>)}</ul>}
      </section>
    </>}
    <p className="agents-runtime__freshness">Agent-reported query times are shown separately. Backend observation acceptance freshness is not available.</p>
  </Panel>;
}

function reportedDate(value: string | null): string {
  return value === null ? 'Not reported' : value.replace('T', ' ');
}
