import { useEffect, useState } from 'react';
import { Combobox, SegmentedControl, type ComboboxOption } from '../components/ui/index.ts';
import { serviceClient } from '../services/client.ts';
import { sloClient } from '../slos/client.ts';
import type { MaintenanceSelector } from './client.ts';

type Kind = MaintenanceSelector['kind'];
export type SelectedKind = Exclude<Kind, 'all_services'>;

export const scopeLabels = {
  services: 'Selected services',
  slos: 'Selected SLOs',
  all_services: 'All services',
} satisfies Record<Kind, string>;

export const maxScope = 100;

export function scopeIDs(scope: MaintenanceSelector): string[] {
  switch (scope.kind) {
    case 'services': return scope.service_ids;
    case 'slos': return scope.slo_ids;
    case 'all_services': return [];
  }
}

export function scopeSelector(kind: Kind, ids: string[]): MaintenanceSelector {
  switch (kind) {
    case 'services': return { kind, service_ids: ids };
    case 'slos': return { kind, slo_ids: ids };
    case 'all_services': return { kind };
  }
}

export async function scopeOption(kind: SelectedKind, id: string, signal: AbortSignal): Promise<ComboboxOption> {
  if (kind === 'services') {
    const { value } = await serviceClient.get(id, signal);
    return { id, label: value.display_name, detail: value.owner };
  }
  const { value } = await sloClient.get(id, signal);
  return { id, label: value.display_name, detail: value.service_display_name };
}

export function ScopeMode({ kind, onChange, disabled }: { kind: Kind; onChange: (kind: Kind) => void; disabled: boolean }) {
  return <SegmentedControl className="maintenance-scope-mode" label="Maintenance scope" value={kind} onChange={onChange}
    options={[
      { value: 'services', label: scopeLabels.services, disabled },
      { value: 'slos', label: scopeLabels.slos, disabled },
      { value: 'all_services', label: scopeLabels.all_services, disabled },
    ]} />;
}

export function ScopePicker({ kind, selected, onChange, disabled }: {
  kind: SelectedKind;
  selected: ComboboxOption[];
  onChange: (selected: ComboboxOption[]) => void;
  disabled: boolean;
}) {
  const [term, setTerm] = useState('');
  const [options, setOptions] = useState<ComboboxOption[]>([]);
  const [error, setError] = useState('');
  const [searching, setSearching] = useState(true);
  const noun = kind === 'services' ? 'services' : 'SLOs';
  const label = `Search ${noun}`;
  const atLimit = selected.length >= maxScope;

  useEffect(() => {
    const controller = new AbortController();
    const query = { q: term.trim(), limit: 10 };
    setSearching(true);
    setError('');
    setOptions([]);
    const timer = window.setTimeout(async () => {
      try {
        const matches = kind === 'services'
          ? (await serviceClient.list(query, controller.signal)).items
            .map(service => ({ id: service.id, label: service.display_name, detail: service.owner }))
          : (await sloClient.overview(query, controller.signal)).items
            .map(({ slo }) => ({ id: slo.id, label: slo.display_name, detail: slo.service_display_name }));
        if (!controller.signal.aborted) setOptions(matches);
      } catch {
        if (!controller.signal.aborted) setError(`${noun === 'SLOs' ? 'SLO' : 'Service'} search is unavailable. Change your search to try again.`);
      } finally {
        if (!controller.signal.aborted) setSearching(false);
      }
    }, term ? 180 : 0);
    return () => { window.clearTimeout(timer); controller.abort(); };
  }, [kind, noun, term]);

  return <Combobox id={`maintenance-${kind}`} label={label} placeholder={kind === 'services' ? 'Name or owner' : 'Name or service'}
    options={options.map(option => atLimit ? { ...option, disabled: true } : option)} value={null} onSearch={setTerm}
    onChange={id => {
      const option = options.find(item => item.id === id);
      if (option && !atLimit) onChange([...selected, option]);
    }}
    multiple={{ selected, placement: 'below', selectionLabel: scopeLabels[kind], onRemove: id => onChange(selected.filter(option => option.id !== id)) }}
    hint={atLimit ? `Remove a selection before adding another. The limit is ${maxScope}.` : undefined}
    error={error || undefined} disabled={disabled}
    noMatch={{ text: () => searching ? `Searching for ${noun}…` : error || `No matching ${noun}. Try a different search.` }} />;
}
