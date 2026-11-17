import { useEffect, useRef, useState, type FormEvent } from 'react';
import type { AuthContext } from '../auth/client.ts';
import { Button, ChoiceChips, Combobox, DatePicker, FormActions, FormSection, PageHeader, TextareaField, TextField, type ComboboxOption } from '../components/ui/index.ts';
import { SloFailure, sloClient } from '../slos/client.ts';
import { maintenanceClient, type MaintenanceWrite } from './client.ts';

const maxScope = 100;

const durations = [
  { value: '15', label: '15 min' },
  { value: '30', label: '30 min' },
  { value: '60', label: '1 hour' },
  { value: '120', label: '2 hours' },
];

export function ScheduleMaintenance({ context }: { context: AuthContext }) {
  const [title, setTitle] = useState('');
  const [reason, setReason] = useState('');
  const [term, setTerm] = useState('');
  const [options, setOptions] = useState<ComboboxOption[]>([]);
  const [selected, setSelected] = useState<ComboboxOption[]>([]);
  const [searchError, setSearchError] = useState('');
  const [searching, setSearching] = useState(true);
  const [day, setDay] = useState('');
  const [time, setTime] = useState('');
  const [duration, setDuration] = useState('60');
  const [error, setError] = useState('');
  const [saving, setSaving] = useState(false);
  const attempt = useRef<{ body: string; key: string } | null>(null);
  const notice = useRef<HTMLParagraphElement>(null);
  const mounted = useRef(false);

  useEffect(() => {
    mounted.current = true;
    return () => { mounted.current = false; };
  }, []);

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
  const end = start + minutes * 60_000;
  const validSchedule = day && time && Number.isFinite(start) && new Date(start).getUTCFullYear() >= 1970 && /^[0-9]+$/.test(duration) && Number.isSafeInteger(minutes) && minutes > 0 &&
    Number.isFinite(new Date(end).getTime()) && new Date(end).getUTCFullYear() <= 9999;

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (saving) return;
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
    const serialized = JSON.stringify(body);
    if (attempt.current?.body !== serialized) attempt.current = { body: serialized, key: crypto.randomUUID() };
    setSaving(true);
    setError('');
    try {
      const result = await maintenanceClient.create(body, context, attempt.current.key);
      if (!mounted.current || location.pathname !== '/maintenance/new') return;
      history.replaceState({}, '', `/maintenance/${result.value.id}`);
      dispatchEvent(new PopStateEvent('popstate'));
    } catch (failure) {
      if (!mounted.current) return;
      if (failure instanceof SloFailure && !failure.uncertain) attempt.current = null;
      setError(failure instanceof Error ? failure.message : 'The maintenance could not be scheduled.');
    } finally {
      if (mounted.current) setSaving(false);
    }
  }

  return <>
    <PageHeader title="Schedule maintenance" breadcrumb={{ href: '/maintenance', label: 'Maintenance', current: 'Schedule' }} />
    <form className="maintenance-form" onSubmit={submit}>
      <FormSection title="Context">
        <TextField id="maintenance-title" label="Name" value={title} onChange={event => setTitle(event.target.value)} maxLength={512} required disabled={saving} />
        <TextareaField id="maintenance-reason" label="Reason" value={reason} onChange={event => setReason(event.target.value)} maxLength={4096} required disabled={saving} />
      </FormSection>
      <FormSection title="Scope">
        <Combobox id="maintenance-slo" label="Affected SLOs" options={options} value={null} onSearch={setTerm}
          onChange={id => { const option = options.find(item => item.id === id); if (option) setSelected(current => current.length < maxScope ? [...current, option] : current); }}
          multiple={{ selected, placement: 'below', selectionLabel: 'Selected SLOs', onRemove: id => setSelected(current => current.filter(option => option.id !== id)) }}
          placeholder="Search SLOs" hint={selected.length === maxScope ? `Remove an SLO before adding another. The limit is ${maxScope}.` : "Measurements in this window are excluded from the selected SLOs."} error={searchError || undefined} disabled={saving}
          noMatch={{ text: () => searching ? 'Searching for SLOs…' : searchError || 'No matching SLOs. Try a different name.' }} />
      </FormSection>
      <FormSection title="Schedule" hint="UTC">
        <div className="maintenance-schedule-row">
          <DatePicker label="Start date" value={day} onValueChange={setDay} disabled={saving} today={new Date().toISOString().slice(0, 10)} />
          <TextField id="maintenance-time" label="Start time (UTC)" type="time" value={time} onChange={event => setTime(event.target.value)} required disabled={saving} />
        </div>
        <div className="maintenance-duration">
          <ChoiceChips id="maintenance-duration" label="Duration" options={durations} value={duration} onChange={setDuration} disabled={saving}
            custom={{ unit: 'minutes', placeholder: 'Minutes', inputMode: 'numeric', format: value => `${value} min`,
              validate: value => /^[0-9]+$/.test(value) && Number.isSafeInteger(Number(value)) && Number(value) > 0 ? undefined : 'Enter a positive whole number of minutes.' }} />
        </div>
        {validSchedule && <p className="ui-hint">Ends <time dateTime={new Date(end).toISOString()}>{new Date(end).toISOString().slice(0, 16).replace('T', ' ')} UTC</time></p>}
      </FormSection>
      {error && <p ref={notice} tabIndex={-1} role="alert" className="ui-error">{error}</p>}
      <FormActions>
        <a data-app-route href="/maintenance" className="ui-button ui-button--outline">Cancel</a>
        <Button type="submit" pending={saving}>Schedule maintenance</Button>
      </FormActions>
    </form>
  </>;
}
