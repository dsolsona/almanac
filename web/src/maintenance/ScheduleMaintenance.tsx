import { useEffect, useMemo, useRef, useState, type FormEvent } from 'react';
import type { AuthContext } from '../auth/client.ts';
import { Button, ChoiceChips, DatePicker, FormActions, FormSection, PageHeader, Panel, TextareaField, TextField, type ComboboxOption } from '../components/ui/index.ts';
import { SloFailure } from '../slos/client.ts';
import { maintenanceClient, type MaintenanceSelector, type MaintenanceWindow, type MaintenanceWrite } from './client.ts';
import { MaintenanceImpactPreview } from './MaintenanceImpact.tsx';
import { maxScope, scopeIDs, scopeLabels, scopeOption, scopeSelector, ScopeMode, ScopePicker, type SelectedKind } from './ScopePicker.tsx';

const emptySelection: ComboboxOption[] = [];

const durations = [
  { value: '15', label: '15 min' },
  { value: '30', label: '30 min' },
  { value: '60', label: '1 hour' },
  { value: '120', label: '2 hours' },
];

export function ScheduleMaintenance({ context, existing }: { context: AuthContext; existing?: { value: MaintenanceWindow; etag: string } }) {
  const windowRecord = existing?.value;
  const originalDuration = windowRecord ? Date.parse(windowRecord.end) - Date.parse(windowRecord.start) : 0;
  const initialDuration = windowRecord ? String(originalDuration / 60_000) : '60';
  const displayDurations = windowRecord && !Number.isInteger(originalDuration / 60_000)
    ? [...durations, { value: initialDuration, label: `${originalDuration / 1000} seconds` }]
    : durations;
  const returnTo = windowRecord ? `/maintenance/${windowRecord.id}` : '/maintenance';
  const [title, setTitle] = useState(windowRecord?.title ?? '');
  const [reason, setReason] = useState(windowRecord?.reason ?? '');
  const [kind, setKind] = useState<MaintenanceSelector['kind']>(windowRecord?.scope.kind ?? 'slos');
  const [selections, setSelections] = useState<Partial<Record<SelectedKind, ComboboxOption[]>>>(() => {
    const scope = windowRecord?.scope;
    return scope && scope.kind !== 'all_services' ? { [scope.kind]: scopeIDs(scope).map(id => ({ id, label: id })) } : {};
  });
  const [scopeNames, setScopeNames] = useState(() => new Map<string, ComboboxOption>());
  const selected = kind === 'all_services' ? emptySelection : selections[kind] ?? emptySelection;
  const scope = useMemo(() => scopeSelector(kind, selected.map(option => option.id).sort()), [kind, selected]);
  const [reviewedScope, setReviewedScope] = useState<MaintenanceSelector>();
  const scopeValid = kind === 'all_services' || selected.length > 0 && selected.length <= maxScope;
  const [day, setDay] = useState(windowRecord?.start.slice(0, 10) ?? '');
  const [time, setTime] = useState(windowRecord?.start.slice(11, 19) ?? '');
  const [duration, setDuration] = useState(initialDuration);
  const [error, setError] = useState('');
  const [saving, setSaving] = useState(false);
  const [etag, setETag] = useState(existing?.etag ?? '');
  const [conflict, setConflict] = useState(false);
  const [latest, setLatest] = useState<{ value: MaintenanceWindow; etag?: string }>();
  const route = useRef(location.pathname + location.search);
  const attempt = useRef<{ body: string; key: string } | null>(null);
  const notice = useRef<HTMLParagraphElement>(null);
  const heading = useRef<HTMLHeadingElement>(null);
  const saveButton = useRef<HTMLButtonElement>(null);
  const mounted = useRef(false);
  const revisionRead = useRef<AbortController | null>(null);

  useEffect(() => {
    mounted.current = true;
    return () => { mounted.current = false; revisionRead.current?.abort(); };
  }, []);

  useEffect(() => {
    const selections = new Map<string, { kind: SelectedKind; id: string }>();
    for (const scope of [windowRecord?.scope, latest?.value.scope]) {
      if (!scope || scope.kind === 'all_services') continue;
      for (const id of scopeIDs(scope)) selections.set(`${scope.kind}:${id}`, { kind: scope.kind, id });
    }
    if (!selections.size) return;
    const controller = new AbortController();
    const entries = [...selections];
    async function resolveNames() {
      for (let offset = 0; offset < entries.length && !controller.signal.aborted; offset += 10) {
        const results = await Promise.allSettled(entries.slice(offset, offset + 10).map(async ([key, { kind, id }]) => {
          return [key, await scopeOption(kind, id, controller.signal)] as const;
        }));
        if (controller.signal.aborted) return;
        const resolved = results.flatMap(result => result.status === 'fulfilled' ? [result.value] : []);
        setScopeNames(current => new Map([...current, ...resolved]));
      }
    }
    void resolveNames();
    return () => controller.abort();
  }, [windowRecord, latest]);

  useEffect(() => { if (error) notice.current?.focus(); }, [error]);
  const editing = Boolean(windowRecord);
  useEffect(() => { if (editing) heading.current?.focus(); }, [editing]);

  const minutes = Number(duration);
  const start = Date.parse(`${day}T${time}Z`);
  const retainedDuration = windowRecord && duration === initialDuration;
  const durationMs = retainedDuration ? originalDuration : minutes * 60_000;
  const end = start + durationMs;
  const validDuration = retainedDuration || /^[0-9]+$/.test(duration) && Number.isSafeInteger(minutes) && minutes > 0;
  const validSchedule = day && time && Number.isFinite(start) && new Date(start).getUTCFullYear() >= 1970 && validDuration &&
    Number.isFinite(new Date(end).getTime()) && new Date(end).getUTCFullYear() <= 9999;

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (saving || conflict) return;
    const bytes = new TextEncoder();
    if (!title.trim() || !reason.trim() || bytes.encode(title.trim()).length > 512 || bytes.encode(reason.trim()).length > 4096) {
      setError('Enter a name and reason within the allowed lengths.');
      return;
    }
    if (!scopeValid) {
      setError(`Select between 1 and ${maxScope} ${kind === 'services' ? 'services' : 'SLOs'}.`);
      return;
    }
    if (reviewedScope !== scope) {
      setError('Wait for the affected SLO preview before saving.');
      return;
    }
    if (!validSchedule) { setError('Choose a valid start time and duration.'); return; }

    const body: MaintenanceWrite = {
      title: title.trim(), reason: reason.trim(), scope,
      start: new Date(start).toISOString(), end: new Date(end).toISOString(),
    };
    const serialized = JSON.stringify([etag, body]);
    if (attempt.current?.body !== serialized) attempt.current = { body: serialized, key: crypto.randomUUID() };
    setSaving(true);
    setError('');
    try {
      const result = windowRecord
        ? await maintenanceClient.update(windowRecord.id, body, context, attempt.current.key, etag)
        : await maintenanceClient.create(body, context, attempt.current.key);
      if (!mounted.current || location.pathname + location.search !== route.current) return;
      history.replaceState({}, '', `/maintenance/${result.value.id}`);
      dispatchEvent(new PopStateEvent('popstate'));
    } catch (failure) {
      if (!mounted.current) return;
      if (failure instanceof SloFailure && !failure.uncertain && failure.code !== 'maintenance_write_retry') {
        attempt.current = null;
        setConflict(!!windowRecord && failure.status === 409);
        setLatest(undefined);
      }
      setError(failure instanceof Error ? failure.message : 'The maintenance could not be scheduled.');
    } finally {
      if (mounted.current) setSaving(false);
    }
  }

  async function readCurrentRevision() {
    if (!windowRecord || saving) return;
    const controller = new AbortController();
    revisionRead.current = controller;
    setSaving(true);
    try {
      const current = await maintenanceClient.get(windowRecord.id, '', controller.signal);
      if (!mounted.current) return;
      const currentTime = current.serverClock?.time ?? Date.now();
      if (current.value.tombstone || Date.parse(current.value.start) <= currentTime) {
        setLatest({ value: current.value });
        setError('The latest revision is no longer scheduled. Open the window to see its current state.');
        return;
      }
      setLatest(current);
      setError('Review the current revision before keeping your draft.');
    } catch (failure) {
      if (mounted.current) setError(failure instanceof Error ? failure.message : 'The latest revision could not be loaded.');
    } finally {
      if (mounted.current) setSaving(false);
    }
  }

  const latestETag = latest?.etag;
  const latestScope = latest?.value.scope;
  const sameKind = latestScope?.kind === kind;
  const draftIDs = scopeIDs(scope);
  const currentIDs = latestScope ? scopeIDs(latestScope) : [];
  const onlyCurrent = sameKind ? currentIDs.filter(id => !draftIDs.includes(id)) : currentIDs;
  const onlyDraft = sameKind ? draftIDs.filter(id => !currentIDs.includes(id)) : draftIDs;
  const labelScope = (mode: MaintenanceSelector['kind'], ids: string[], showMode: boolean) => <>
    {showMode && <p>{scopeLabels[mode]}</p>}
    {ids.length > 0 && <ul>{ids.map(id => {
      const option = scopeNames.get(`${mode}:${id}`) ?? (mode === kind ? selected.find(option => option.id === id) : undefined);
      return <li key={id}>{option?.label ?? id}{option?.detail && <small> · {option.detail}</small>}</li>;
    })}</ul>}
  </>;

  return <>
    <PageHeader headingRef={heading} title={windowRecord ? 'Edit maintenance schedule' : 'Schedule maintenance'}
      breadcrumb={{ href: returnTo, label: windowRecord?.title ?? 'Maintenance', current: windowRecord ? 'Edit schedule' : 'Schedule' }} />
    <form className="maintenance-form" onSubmit={submit}>
      <FormSection title="Context">
        <TextField id="maintenance-title" label="Name" value={title} onChange={event => setTitle(event.target.value)} maxLength={512} required disabled={saving} />
        <TextareaField id="maintenance-reason" label="Reason" value={reason} onChange={event => setReason(event.target.value)} maxLength={4096} required disabled={saving} />
      </FormSection>
      <FormSection title="Scope">
        <ScopeMode kind={kind} onChange={setKind} disabled={saving} />
        {kind === 'all_services'
          ? <p className="ui-hint">Covers all services and eligible SLOs that join during the window.</p>
          : <ScopePicker key={kind} kind={kind} selected={selected.map(option => scopeNames.get(`${kind}:${option.id}`) ?? option)}
            onChange={options => setSelections(current => ({ ...current, [kind]: options }))} disabled={saving} />}
        {scopeValid && <MaintenanceImpactPreview key={JSON.stringify(scope)} scope={scope} context={context} onReady={setReviewedScope} />}
      </FormSection>
      <FormSection title="Schedule" hint="UTC">
        <div className="maintenance-schedule-row">
          <DatePicker label="Start date" value={day} onValueChange={setDay} disabled={saving} today={new Date().toISOString().slice(0, 10)} />
          <TextField id="maintenance-time" label="Start time (UTC)" type="time" step={windowRecord ? 1 : 60} value={time} onChange={event => setTime(event.target.value)} required disabled={saving} />
        </div>
        <div className="maintenance-duration">
          <ChoiceChips id="maintenance-duration" label="Duration" options={displayDurations} value={duration} onChange={setDuration} disabled={saving}
            custom={{ unit: 'minutes', placeholder: 'Minutes', inputMode: 'numeric', format: value => `${value} min`,
              validate: value => /^[0-9]+$/.test(value) && Number.isSafeInteger(Number(value)) && Number(value) > 0 ? undefined : 'Enter a positive whole number of minutes.' }} />
        </div>
        {validSchedule && <p className="ui-hint">Ends <time dateTime={new Date(end).toISOString()}>{new Date(end).toISOString().slice(0, windowRecord ? 19 : 16).replace('T', ' ')} UTC</time></p>}
      </FormSection>
      {error && <p ref={notice} tabIndex={-1} role="alert" className="ui-error">{error}</p>}
      {latest && <Panel heading={`Current revision ${latest.value.revision}`} className="maintenance-detail">
        <dl>
          <dt>Name</dt><dd>{latest.value.title}</dd>
          <dt>Reason</dt><dd className="maintenance-reason">{latest.value.reason}</dd>
          <dt>Schedule</dt><dd><time>{latest.value.start}</time> to <time>{latest.value.end}</time></dd>
          {latestScope && (!sameKind || onlyCurrent.length > 0) && <><dt>Only in the current revision</dt><dd>{labelScope(latestScope.kind, onlyCurrent, !sameKind)}</dd></>}
          {(!sameKind || onlyDraft.length > 0) && <><dt>Only in your draft</dt><dd>{labelScope(kind, onlyDraft, !sameKind)}</dd></>}
        </dl>
        {latest.etag && <p className="ui-hint">Keeping your draft lets you save it over this revision.</p>}
      </Panel>}
      <FormActions>
        <a data-app-route href={returnTo} className="ui-button ui-button--outline">Cancel</a>
        {conflict && !latestETag && <Button variant="outline" disabled={saving} onClick={() => void readCurrentRevision()}>Read latest revision</Button>}
        {conflict && latestETag && <Button variant="outline" onClick={() => {
          setETag(latestETag);
          setConflict(false);
          setLatest(undefined);
          setError('');
          requestAnimationFrame(() => saveButton.current?.focus());
        }}>Keep my draft</Button>}
        <Button ref={saveButton} type="submit" pending={saving} disabled={conflict || scopeValid && reviewedScope !== scope}>{windowRecord ? 'Save schedule' : 'Schedule maintenance'}</Button>
      </FormActions>
    </form>
  </>;
}
