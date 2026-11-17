import { useEffect, useRef, useState, type FormEvent, type RefObject } from 'react';
import type { components } from '../api/generated.ts';
import type { AuthContext } from '../auth/client.ts';
import { Button, DatePicker, Dialog, FormActions, TextareaField, TextField } from '../components/ui/index.ts';
import { request, SloFailure, write } from '../slos/client.ts';

type Window = components['schemas']['MaintenanceWindow'];
type Action = 'cancel' | 'end' | 'withdraw';

const actions = {
  cancel: {
    label: 'Cancel scheduled maintenance',
    title: 'Cancel this maintenance window?',
    explanation: 'The window will be cancelled before it starts. The cancellation appears in its revision history.',
    confirm: 'Cancel maintenance',
  },
  end: {
    label: 'End maintenance early',
    title: 'End this maintenance early?',
    explanation: 'Exclusions before the new end time still apply. Measurements from the new end time onwards count again.',
    confirm: 'End maintenance',
  },
  withdraw: {
    label: 'Include maintenance events…',
    title: 'Include maintenance events in SLO calculations?',
    explanation: 'Both successful and failed events from this window will count again. Failures may lower attainment and reduce the remaining error budget. Previously exported reports retain their original calculations.',
    confirm: 'Include maintenance events',
  },
} as const;

type Props = {
  context: AuthContext;
  record: Window;
  etag: string;
  now: number;
  onUpdated: (result: { value: Window; etag: string }) => void;
};

export function MaintenanceLifecycle({ context, record, etag, now, onUpdated, historical = false }: Props & { historical?: boolean }) {
  const [action, setAction] = useState<Action>();
  const trigger = useRef<HTMLElement>(null);
  const canWrite = context.session?.role === 'administrator' || context.session?.role === 'editor';
  if (!canWrite || record.tombstone) return null;
  const upcoming = Date.parse(record.start) > now;
  const active = !upcoming && Date.parse(record.end) > now;
  const available: Action[] = historical
    ? !upcoming && context.session?.role === 'administrator' ? ['withdraw'] : []
    : upcoming ? ['cancel'] : active ? ['end'] : [];
  if (!available.length) return null;

  const controls = <>
    {available.map(value => <Button key={value} variant={value === 'withdraw' ? 'danger-outline' : 'outline'} onClick={event => {
      trigger.current = event.currentTarget;
      setAction(value);
    }}>{actions[value].label}</Button>)}
    <LifecycleDialog key={action} action={action} context={context} record={record} etag={etag} now={now} trigger={trigger}
      onClose={() => {
        setAction(undefined);
        requestAnimationFrame(() => trigger.current?.focus());
      }} onUpdated={result => { setAction(undefined); onUpdated(result); }} />
  </>;
  return historical ? <details className="maintenance-panel maintenance-detail maintenance-correction">
    <summary>Change historical SLO calculations</summary>
    <p>Administrators can include the measurements from this window in future calculations.</p>
    {controls}
  </details> : controls;
}

function LifecycleDialog({ action, context, record, etag, now, trigger, onClose, onUpdated }:
  Props & { action: Action | undefined; trigger: RefObject<HTMLElement | null>; onClose: () => void }) {
  const [day, setDay] = useState(() => new Date(now).toISOString().slice(0, 10));
  const [time, setTime] = useState('');
  const [reason, setReason] = useState('');
  const [error, setError] = useState('');
  const [conflict, setConflict] = useState(false);
  const [sending, setSending] = useState(false);
  const [revision, setRevision] = useState(etag);
  const [latest, setLatest] = useState<Window>();
  const attempt = useRef<{ identity: string; key: string } | undefined>(undefined);
  const notice = useRef<HTMLParagraphElement>(null);

  useEffect(() => { if (error) notice.current?.focus(); }, [error]);
  if (!action) return null;
  const operation = actions[action];
  const current = latest ?? record;

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!action || sending || conflict) return;
    const text = reason.trim();
    if (!text || new TextEncoder().encode(text).length > 4096) {
      setError('Enter a reason within the allowed length.');
      return;
    }
    const body: { reason: string; end?: string } = { reason: text };
    if (action === 'end') {
      const end = Date.parse(`${day}T${time}Z`);
      if (!Number.isFinite(end) || end <= Math.max(now, Date.parse(current.start)) || end >= Date.parse(current.end)) {
        setError('Choose a future time before the scheduled end.');
        return;
      }
      body.end = new Date(end).toISOString();
    }
    const identity = JSON.stringify([revision, body]);
    if (attempt.current?.identity !== identity) attempt.current = { identity, key: crypto.randomUUID() };
    setSending(true);
    setError('');
    try {
      const result = await request<Window>(`/api/v1/maintenance/${record.id}/${action}`,
        { ...write(context, body, attempt.current.key, revision), method: 'POST' }, true);
      onUpdated(result);
    } catch (failure) {
      setConflict(failure instanceof SloFailure && failure.status === 409);
      if (failure instanceof SloFailure && !failure.uncertain) attempt.current = undefined;
      setError(failure instanceof Error ? failure.message : 'The change could not be saved.');
    } finally {
      setSending(false);
    }
  }

  async function checkRevision() {
    if (sending) return;
    setSending(true);
    try {
      const result = await request<Window>(`/api/v1/maintenance/${record.id}`);
      setLatest(result.value);
      setRevision(result.etag);
      setConflict(result.value.tombstone);
      setError(result.value.tombstone ? 'The latest revision has already closed this window.' : `Latest revision ${result.value.revision} loaded. Check the dates before confirming your change.`);
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : 'The latest revision could not be loaded.');
    } finally {
      setSending(false);
    }
  }

  return <Dialog open onClose={() => { if (!sending) onClose(); }} title={operation.title} triggerRef={trigger}>
    <form onSubmit={submit} className="maintenance-lifecycle-form">
      <p>{operation.explanation}</p>
      {latest && <p>Current schedule <time dateTime={latest.start}>{latest.start}</time> to <time dateTime={latest.end}>{latest.end}</time></p>}
      {action === 'end' && <div className="maintenance-schedule-row">
        <DatePicker label="New end date (UTC)" value={day} onValueChange={setDay} disabled={sending} />
        <TextField id="maintenance-end-time" label="New end time (UTC)" type="time" value={time} onChange={event => setTime(event.target.value)} required disabled={sending} />
      </div>}
      <TextareaField id="maintenance-change-reason" label="Reason for this change" value={reason} onChange={event => setReason(event.target.value)} required maxLength={4096} disabled={sending} />
      {error && <p ref={notice} tabIndex={-1} role="alert" className="ui-error">{error}</p>}
      <FormActions>
        <Button variant="outline" onClick={onClose} disabled={sending}>Close</Button>
        {conflict && <Button variant="outline" onClick={() => void checkRevision()} disabled={sending}>Read latest revision</Button>}
        <Button type="submit" variant="danger" pending={sending} disabled={conflict}>{operation.confirm}</Button>
      </FormActions>
    </form>
  </Dialog>;
}
