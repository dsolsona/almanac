import { useEffect, useRef, useState, type FormEvent, type RefObject } from 'react';
import type { AuthContext } from '../auth/client.ts';
import { Button, DatePicker, Dialog, FormActions, TextareaField, TextField } from '../components/ui/index.ts';
import { SloFailure, type ServerClock } from '../slos/client.ts';
import { maintenanceClient, type MaintenanceWindow as Window } from './client.ts';
import { MaintenanceScope } from './MaintenanceScope.tsx';

type WindowRevision = { value: Window; etag: string; serverClock?: ServerClock };
type Action = 'cancel' | 'end' | 'withdraw';

const actions = {
  cancel: {
    label: 'Cancel scheduled maintenance',
    title: 'Cancel this maintenance window?',
    explanation: 'The window will be cancelled before it starts. The cancellation appears in its revision history.',
    confirm: 'Cancel maintenance',
    unavailable: 'This window is no longer scheduled.',
  },
  end: {
    label: 'End maintenance early',
    title: 'End this maintenance early?',
    explanation: 'This window stops excluding measurements at the new end time. Other maintenance exclusions still apply.',
    confirm: 'End maintenance',
    unavailable: 'This window is no longer in progress.',
  },
  withdraw: {
    label: 'Include maintenance events…',
    title: 'Include maintenance events in SLO calculations?',
    explanation: 'This removes this window’s exclusion from both successful and failed events. Other maintenance exclusions still apply. Newly included failures may lower attainment and reduce the remaining error budget. Previously exported reports retain their original calculations.',
    confirm: 'Include maintenance events',
    unavailable: 'This historical correction is no longer available.',
  },
} as const;

type Props = {
  context: AuthContext;
  record: Window;
  etag: string;
  now: number;
  readNow: () => number;
  onUpdated: (result: WindowRevision) => void;
  onRevisionRead: (result: WindowRevision) => void;
};

function availableAction(context: AuthContext, record: Window, now: number, historical: boolean): Action | undefined {
  const canWrite = context.session?.role === 'administrator' || context.session?.role === 'editor';
  if (!canWrite || record.tombstone) return undefined;
  const upcoming = Date.parse(record.start) > now;
  const active = !upcoming && Date.parse(record.end) > now;
  if (historical) return !upcoming && context.session?.role === 'administrator' ? 'withdraw' : undefined;
  return upcoming ? 'cancel' : active ? 'end' : undefined;
}

export function MaintenanceLifecycle({ context, record, etag, now, readNow, onUpdated, onRevisionRead, fallbackFocus, historical = false }:
  Props & { historical?: boolean; fallbackFocus: RefObject<HTMLElement | null> }) {
  const [action, setAction] = useState<Action>();
  const trigger = useRef<HTMLElement>(null);
  const available = availableAction(context, record, now, historical);
  if (!available && !action) return null;

  const controls = <>
    {available && <Button variant={available === 'withdraw' ? 'danger-outline' : 'outline'} onClick={event => {
      trigger.current = event.currentTarget;
      setAction(available);
    }}>{actions[available].label}</Button>}
    {action && <LifecycleDialog key={action} action={action} context={context} record={record} etag={etag} readNow={readNow} trigger={trigger} onRevisionRead={onRevisionRead}
      onClose={() => {
        setAction(undefined);
        requestAnimationFrame(() => (trigger.current?.isConnected ? trigger.current : fallbackFocus.current)?.focus());
      }} onUpdated={result => { setAction(undefined); onUpdated(result); }} />}
  </>;
  return historical ? <details className="maintenance-disclosure">
    <summary>Change historical SLO calculations</summary>
    <p>Administrators can remove this window’s exclusion from future calculations.</p>
    {controls}
  </details> : controls;
}

function LifecycleDialog({ action, context, record, etag, readNow, trigger, onClose, onUpdated, onRevisionRead }:
  Omit<Props, 'now'> & { action: Action; trigger: RefObject<HTMLElement | null>; onClose: () => void }) {
  const [day, setDay] = useState(() => new Date(readNow()).toISOString().slice(0, 10));
  const [time, setTime] = useState('');
  const [reason, setReason] = useState('');
  const [error, setError] = useState('');
  const [validation, setValidation] = useState<{ field: 'reason' | 'end'; message: string }>();
  const [outcome, setOutcome] = useState<'conflict' | 'uncertain'>();
  const [sending, setSending] = useState(false);
  const [reading, setReading] = useState(false);
  const [revision, setRevision] = useState(etag);
  const [latest, setLatest] = useState<Window>();
  const attempt = useRef<{ identity: string; key: string } | undefined>(undefined);
  const notice = useRef<HTMLParagraphElement>(null);
  const revisionRead = useRef<AbortController>(undefined);

  const message = [error, validation?.message].filter(Boolean).join(' ');
  useEffect(() => { if (message) notice.current?.focus(); }, [message]);
  useEffect(() => () => revisionRead.current?.abort(), []);
  const operation = actions[action];
  const current = latest ?? record;
  const applies = (now: number) => availableAction(context, current, now, action === 'withdraw') === action;
  const unavailable = !applies(readNow());
  const unavailableMessage = operation.unavailable;

  function clearValidation(field: 'reason' | 'end') {
    setValidation(previous => previous?.field === field ? undefined : previous);
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (sending || reading || outcome === 'conflict') return;
    const now = readNow();
    if (!applies(now)) {
      setError(unavailableMessage);
      return;
    }
    const text = reason.trim();
    if (!text || new TextEncoder().encode(text).length > 4096) {
      setValidation({ field: 'reason', message: 'Enter a reason within the allowed length.' });
      return;
    }
    const body: { reason: string; end?: string } = { reason: text };
    if (action === 'end') {
      const end = Date.parse(`${day}T${time}Z`);
      if (!Number.isFinite(end) || end <= Math.max(now, Date.parse(current.start)) || end >= Date.parse(current.end)) {
        setValidation({ field: 'end', message: 'Choose a future time before the scheduled end.' });
        return;
      }
      body.end = new Date(end).toISOString();
    }
    const identity = JSON.stringify([revision, body]);
    if (attempt.current?.identity !== identity) attempt.current = { identity, key: crypto.randomUUID() };
    setSending(true);
    setError('');
    setValidation(undefined);
    try {
      const result = await maintenanceClient.transition(record.id, action, body, context, attempt.current.key, revision);
      onUpdated(result);
    } catch (failure) {
      const uncertain = failure instanceof SloFailure && failure.uncertain;
      setOutcome(uncertain ? 'uncertain' : failure instanceof SloFailure && failure.status === 409 ? 'conflict' : undefined);
      if (failure instanceof SloFailure && !uncertain) attempt.current = undefined;
      setError(uncertain ? 'The change may have been saved. Read the latest revision to confirm.' :
        failure instanceof Error ? failure.message : 'The change could not be saved.');
    } finally {
      setSending(false);
    }
  }

  async function checkRevision() {
    if (sending || reading) return;
    const controller = new AbortController();
    const timeout = AbortSignal.timeout(30_000);
    revisionRead.current = controller;
    setReading(true);
    try {
      const result = await maintenanceClient.get(record.id, '', AbortSignal.any([controller.signal, timeout]));
      if (controller.signal.aborted) return;
      setLatest(result.value);
      setRevision(result.etag);
      setOutcome(undefined);
      setError('');
      onRevisionRead(result);
    } catch (failure) {
      if (controller.signal.aborted) return;
      setError(timeout.aborted ? 'The latest revision took too long to load. Try again.' :
        failure instanceof Error ? failure.message : 'The latest revision could not be loaded.');
    } finally {
      if (!controller.signal.aborted) setReading(false);
    }
  }

  function close() {
    if (sending) return;
    revisionRead.current?.abort();
    onClose();
  }

  return <Dialog open onClose={close} title={operation.title} triggerRef={trigger}>
    <form onSubmit={submit} className="maintenance-lifecycle-form">
      <p>{operation.explanation}</p>
      {latest && <section aria-label="Current revision">
        <p>Current schedule <time dateTime={latest.start}>{latest.start}</time> to <time dateTime={latest.end}>{latest.end}</time></p>
        <MaintenanceScope key={latest.revision} scope={latest.scope} />
      </section>}
      {latest && !unavailable && <p role="status">Latest revision {latest.revision} loaded. Check the dates before confirming your change.</p>}
      {unavailable && error !== unavailableMessage && <p role="status">{unavailableMessage}</p>}
      {action === 'end' && <div className="maintenance-schedule-row">
        <DatePicker label="New end date (UTC)" value={day} onValueChange={value => { setDay(value); clearValidation('end'); }} disabled={sending} />
        <TextField id="maintenance-end-time" label="New end time (UTC)" type="time" step={1} value={time} onChange={event => { setTime(event.target.value); clearValidation('end'); }} required disabled={sending} />
      </div>}
      <TextareaField id="maintenance-change-reason" label="Reason for this change" value={reason} onChange={event => { setReason(event.target.value); clearValidation('reason'); }} required maxLength={4096} disabled={sending} />
      {message && <p ref={notice} tabIndex={-1} role="alert" className="ui-error">{message}</p>}
      <FormActions>
        <Button variant="outline" onClick={close} disabled={sending}>Close</Button>
        {outcome && <Button variant="outline" onClick={() => void checkRevision()} pending={reading} disabled={sending}>Read latest revision</Button>}
        <Button type="submit" variant="danger" pending={sending} disabled={reading || outcome === 'conflict' || unavailable}>{operation.confirm}</Button>
      </FormActions>
    </form>
  </Dialog>;
}
