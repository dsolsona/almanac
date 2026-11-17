import { useEffect, useRef, useState, type FormEvent } from 'react';
import type { AuthContext } from '../auth/client.ts';
import { Button, ChoiceChips, Combobox, DatePicker, FormActions, FormSection, PageHeader, Panel, TextareaField, TextField, type ComboboxOption } from '../components/ui/index.ts';
import { SloFailure, sloClient } from '../slos/client.ts';
import { maintenanceClient, type MaintenanceWindow, type MaintenanceWrite } from './client.ts';

const maxScope = 100;

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
  const [term, setTerm] = useState('');
  const [options, setOptions] = useState<ComboboxOption[]>([]);
  const [selected, setSelected] = useState<ComboboxOption[]>(() => windowRecord?.slo_ids.map(id => ({ id, label: id })) ?? []);
  const [scopeNames, setScopeNames] = useState(() => new Map<string, ComboboxOption>());
  const [searchError, setSearchError] = useState('');
  const [searching, setSearching] = useState(true);
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
  const saveButton = useRef<HTMLButtonElement>(null);
  const mounted = useRef(false);
  const revisionRead = useRef<AbortController | null>(null);

  useEffect(() => {
    mounted.current = true;
    return () => { mounted.current = false; revisionRead.current?.abort(); };
  }, []);

  useEffect(() => {
    const ids = [...new Set([...(latest?.value.slo_ids ?? []), ...(windowRecord?.slo_ids ?? [])])];
    if (!ids.length) return;
    const controller = new AbortController();
    async function resolveNames(ids: string[]) {
      for (let offset = 0; offset < ids.length && !controller.signal.aborted; offset += 10) {
        const results = await Promise.allSettled(ids.slice(offset, offset + 10).map(id => sloClient.get(id, controller.signal)));
        if (controller.signal.aborted) return;
        const resolved = results.flatMap(result => {
          if (result.status === 'rejected') return [];
          const slo = result.value.value;
          return [{ id: slo.id, label: slo.display_name, detail: slo.service_display_name }];
        });
        setScopeNames(current => new Map<string, ComboboxOption>([...current, ...resolved.map(option => [option.id, option] as const)]));
      }
    }
    void resolveNames(ids);
    return () => controller.abort();
  }, [windowRecord, latest]);

  useEffect(() => {
    const controller = new AbortController();
    setSearching(true);
    setSearchError('');
    setOptions([]);
    const timer = window.setTimeout(() => {
      sloClient.overview({ q: term.trim(), limit: 10 }, controller.signal).then(page => {
        if (controller.signal.aborted) return;
        setOptions(page.items.map(({ slo }) => ({ id: slo.id, label: slo.display_name, detail: slo.service_display_name })));
      }).catch(() => {
        if (!controller.signal.aborted) setSearchError('SLO search is unavailable. Change your search to try again.');
      }).finally(() => {
        if (!controller.signal.aborted) setSearching(false);
      });
    }, term ? 180 : 0);
    return () => { window.clearTimeout(timer); controller.abort(); };
  }, [term]);

  useEffect(() => { if (error) notice.current?.focus(); }, [error]);

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
    if (!selected.length || selected.length > maxScope) { setError(`Select between 1 and ${maxScope} SLOs.`); return; }
    if (!validSchedule) { setError('Choose a valid start time and duration.'); return; }

    const body: MaintenanceWrite = {
      title: title.trim(), reason: reason.trim(), slo_ids: selected.map(option => option.id).sort(),
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
      if (failure instanceof SloFailure && !failure.uncertain) {
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
  const draftScope = new Set(selected.map(option => option.id));
  const currentScope = new Set(latest?.value.slo_ids);
  const onlyCurrent = [...currentScope].filter(id => !draftScope.has(id));
  const onlyDraft = [...draftScope].filter(id => !currentScope.has(id));
  const labelScope = (ids: string[]) => <ul>{ids.map(id => {
    const objective = scopeNames.get(id) ?? selected.find(option => option.id === id);
    return <li key={id}>{objective?.label ?? id}{objective?.detail && <small> · {objective.detail}</small>}</li>;
  })}</ul>;

  return <>
    <PageHeader title={windowRecord ? 'Edit maintenance schedule' : 'Schedule maintenance'}
      breadcrumb={{ href: returnTo, label: windowRecord?.title ?? 'Maintenance', current: windowRecord ? 'Edit schedule' : 'Schedule' }} />
    <form className="maintenance-form" onSubmit={submit}>
      <FormSection title="Context">
        <TextField id="maintenance-title" label="Name" value={title} onChange={event => setTitle(event.target.value)} maxLength={512} required disabled={saving} />
        <TextareaField id="maintenance-reason" label="Reason" value={reason} onChange={event => setReason(event.target.value)} maxLength={4096} required disabled={saving} />
      </FormSection>
      <FormSection title="Scope">
        <Combobox id="maintenance-slo" label="Affected SLOs" options={options} value={null} onSearch={setTerm}
          onChange={id => { const option = options.find(item => item.id === id); if (option) setSelected(current => current.length < maxScope ? [...current, option] : current); }}
          multiple={{ selected: selected.map(option => scopeNames.get(option.id) ?? option), placement: 'below', selectionLabel: 'Selected SLOs', onRemove: id => setSelected(current => current.filter(option => option.id !== id)) }}
          placeholder="Search SLOs" hint={selected.length === maxScope ? `Remove an SLO before adding another. The limit is ${maxScope}.` : "Measurements in this window are excluded from the selected SLOs."} error={searchError || undefined} disabled={saving}
          noMatch={{ text: () => searching ? 'Searching for SLOs…' : searchError || 'No matching SLOs. Try a different name.' }} />
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
          {!!onlyCurrent.length && <><dt>Only in the current revision</dt><dd>{labelScope(onlyCurrent)}</dd></>}
          {!!onlyDraft.length && <><dt>Only in your draft</dt><dd>{labelScope(onlyDraft)}</dd></>}
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
        <Button ref={saveButton} type="submit" pending={saving} disabled={conflict}>{windowRecord ? 'Save schedule' : 'Schedule maintenance'}</Button>
      </FormActions>
    </form>
  </>;
}
