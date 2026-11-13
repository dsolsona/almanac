import { useEffect, useId, useRef, useState, type FormEvent } from 'react';
import { Button, Combobox, FormActions, FormSection, SegmentedControl, TextField, type ComboboxOption } from '../components/ui/index.ts';
import { InlineValue } from '../components/ui/InlineValue.tsx';
import { conditionDescriptions, conditionLabels, conditionPreset, defaultAlertConditions, formatAlertDuration, normalizeAlertThreshold, parseAlertDuration, validateAlertThreshold, type AlertConditionDraft, type AlertConditionPreset, type AlertPolicyDraft, type AlertSeverity } from './policyDraft.ts';
import './policy-form.css';

export type AlertSloOption = ComboboxOption & { target: string; cadenceSeconds: number; windowSeconds: number };
type RawCondition = AlertConditionDraft & { shortText: string; longText: string };
type Field = 'short' | 'long' | 'threshold';
type Props = { slos: AlertSloOption[]; onSearch?: (term: string) => void; onSave: (draft: AlertPolicyDraft) => void; onCancel: () => void; pending?: boolean; error?: string };
const fields: Field[] = ['short', 'long', 'threshold'];
const fieldLabels = { short: 'Short window', long: 'Long window', threshold: 'Burn threshold' };
const rawCondition = (condition: AlertConditionDraft): RawCondition => ({ ...condition, shortText: condition.shortSeconds ? formatAlertDuration(condition.shortSeconds) : '', longText: condition.longSeconds ? formatAlertDuration(condition.longSeconds) : '' });

export function AlertPolicyForm({ slos, onSearch, onSave, onCancel, pending = false, error }: Props) {
  const prefix = useId();
  const [name, setName] = useState(''), [sloId, setSloId] = useState<string | null>(null), [severity, setSeverity] = useState<AlertSeverity>('high');
  const [conditions, setConditions] = useState(() => defaultAlertConditions().map(rawCondition));
  const [active, setActive] = useState<string | null>(null), [errors, setErrors] = useState<Record<string, string>>({});
  const [menuOpen, setMenuOpen] = useState(false), [confirmed, setConfirmed] = useState(false);
  const form = useRef<HTMLFormElement>(null), addButton = useRef<HTMLButtonElement>(null), menu = useRef<HTMLDivElement>(null);
  const errorSummary = useRef<HTMLParagraphElement>(null);
  const selected = slos.find(slo => slo.id === sloId), cadence = selected?.cadenceSeconds ?? 60;
  const key = (condition: RawCondition, field: Field) => `${condition.id}-${field}`;
  const durationError = `Use whole minutes from 1 min to 30 d${cadence === 60 ? '' : `, aligned to the ${cadence}-second source cadence`}.`;
  function parsed(condition: RawCondition, grid = cadence): AlertConditionDraft | null {
    const shortSeconds = parseAlertDuration(condition.shortText, condition.shortSeconds || 60, grid);
    const longSeconds = parseAlertDuration(condition.longText, condition.longSeconds || 60, grid);
    const threshold = normalizeAlertThreshold(condition.threshold);
    return shortSeconds && longSeconds && threshold ? { id: condition.id, shortSeconds, longSeconds, threshold } : null;
  }
  const kind = (condition: RawCondition) => { const value = parsed(condition, 60); return value ? conditionPreset(value) : 'custom'; };
  const nonThirtyDayPresets = selected && selected.windowSeconds !== 2592000 && conditions.some(condition => kind(condition) !== 'custom');

  useEffect(() => { if (error) errorSummary.current?.focus(); }, [error]);

  useEffect(() => {
    if (!menuOpen) return;
    menu.current?.querySelector<HTMLButtonElement>('button:not(:disabled)')?.focus();
    menu.current?.scrollIntoView?.({ block: 'nearest' });
    function outside(event: PointerEvent) {
      if (event.target instanceof Node && !menu.current?.contains(event.target) && !addButton.current?.contains(event.target)) setMenuOpen(false);
    }
    document.addEventListener('pointerdown', outside);
    return () => document.removeEventListener('pointerdown', outside);
  }, [menuOpen]);

  function commit(condition: RawCondition, field: Field, value: string) {
    const next = { ...condition };
    if (field === 'threshold') next.threshold = value;
    else {
      const seconds = parseAlertDuration(value, condition[field === 'short' ? 'shortSeconds' : 'longSeconds'] || 60, cadence);
      if (field === 'short') { next.shortText = value; if (seconds) next.shortSeconds = seconds; }
      else { next.longText = value; if (seconds) next.longSeconds = seconds; }
    }
    setConditions(current => current.map(item => item.id === condition.id ? next : item));
  }
  function changeText(condition: RawCondition, field: Field, value: string) {
    // Keep the submitted draft current while typing. Defer unit normalization to
    // commit so bare values keep the unit with which this edit began.
    const property = field === 'short' ? 'shortText' : field === 'long' ? 'longText' : 'threshold';
    setConditions(current => current.map(item => item.id === condition.id ? { ...item, [property]: value } : item));
    setErrors(current => { const result = { ...current }; delete result[key(condition, field)]; return result; });
  }

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); if (pending) return;
    const problems: Record<string, string> = {}, values: AlertConditionDraft[] = [];
    if (!name.trim()) problems.name = 'Enter an alert policy name.';
    if (!selected || selected.disabled) problems.slo = 'Choose a supported SLO.';
    for (const condition of conditions) {
      if (!parseAlertDuration(condition.shortText, condition.shortSeconds || 60, cadence)) problems[key(condition, 'short')] = durationError;
      if (!parseAlertDuration(condition.longText, condition.longSeconds || 60, cadence)) problems[key(condition, 'long')] = durationError;
      const thresholdError = validateAlertThreshold(condition.threshold, selected?.target);
      if (thresholdError) problems[key(condition, 'threshold')] = thresholdError;
      const value = parsed(condition);
      if (value) {
        if (value.shortSeconds > value.longSeconds) problems[key(condition, 'long')] = 'The long window must be at least as long as the short window.';
        values.push(value);
      }
    }
    if (nonThirtyDayPresets && !confirmed) problems.confirm = 'Confirm these presets for this SLO, or customize the conditions.';
    setErrors(problems);
    const first = Object.keys(problems)[0];
    if (first) {
      if (first !== 'name' && first !== 'slo' && first !== 'confirm') setActive(first);
      else requestAnimationFrame(() => document.getElementById(`${prefix}-${first}`)?.focus());
      return;
    }
    onSave({ name: name.trim(), sloId: selected!.id, severity, conditions: values, confirmNonThirtyDayPresets: Boolean(nonThirtyDayPresets && confirmed) });
  }
  function addCondition(preset: AlertConditionPreset) {
    const value = preset === 'custom' ? { id: crypto.randomUUID(), shortSeconds: 0, longSeconds: 0, threshold: '' } : defaultAlertConditions()[preset === 'fast' ? 0 : 1]!;
    setConditions(current => [...current, rawCondition(value)]); setMenuOpen(false);
    if (preset === 'custom') setActive(`${value.id}-short`);
    else addButton.current?.focus();
  }
  return <form ref={form} className="alert-policy-form" onSubmit={submit} noValidate aria-label="Alert policy" aria-busy={pending || undefined}>
    {error && <p ref={errorSummary} className="ui-error alert-policy-error" role="alert" tabIndex={-1}>{error}</p>}
    <fieldset disabled={pending} className="alert-policy-fields">
      <FormSection step={1} title="Policy">
        <div className="alert-policy-identity">
          <TextField id={`${prefix}-name`} label="Alert policy name" value={name} onChange={event => setName(event.target.value)} error={errors.name} autoComplete="off" />
          <Combobox id={`${prefix}-slo`} label="SLO" options={slos} value={sloId} onChange={id => { setSloId(id); setConfirmed(false); }} onSearch={onSearch}
            selectionPresentation="compact" placeholder="Search SLOs…" hint={selected?.detail ?? 'Select an SLO to view its target.'} error={errors.slo}
            noMatch={{ text: () => 'No SLOs found.' }} />
          <div className="ui-field"><span className="ui-label">Severity</span><SegmentedControl label="Severity" value={severity} onChange={setSeverity}
            options={[{ value: 'low', label: 'Low' }, { value: 'medium', label: 'Medium' }, { value: 'high', label: 'High' }]} /></div>
        </div>
      </FormSection>
      <FormSection step={2} title="Conditions">
        <p className="alert-condition-intro">An alert fires when the burn rate reaches the threshold in <strong>both windows of any one condition</strong>.</p>
        <div className="alert-condition-heading" aria-hidden="true"><span>Condition</span><span>Short window</span><span>Long window</span><span>Burn threshold</span><span /></div>
        <div className="alert-condition-rows">{conditions.map(condition => {
          const preset = kind(condition), label = conditionLabels[preset];
          return <div className="alert-condition-row" key={condition.id}>
            <div className="alert-condition-identity"><strong>{label}</strong><small>{conditionDescriptions[preset]}</small></div>
            {fields.map(field => <div className="alert-condition-field" key={field}>
              <span className="alert-condition-mobile-label" aria-hidden="true">{fieldLabels[field]}</span>
              <InlineValue id={`${prefix}-${key(condition, field)}`} label={`${fieldLabels[field]} for ${label}`} value={field === 'short' ? condition.shortText : field === 'long' ? condition.longText : condition.threshold}
                editing={active === key(condition, field)} onEditingChange={editing => setActive(current => editing ? key(condition, field) : current === key(condition, field) ? null : current)}
                onCommit={value => commit(condition, field, value)} onTextChange={value => changeText(condition, field, value)} error={errors[key(condition, field)]} suffix={field === 'threshold' ? '×' : undefined}
                normalize={value => {
                  if (field === 'threshold') return normalizeAlertThreshold(value);
                  const seconds = parseAlertDuration(value, condition[field === 'short' ? 'shortSeconds' : 'longSeconds'] || 60, cadence);
                  return seconds ? formatAlertDuration(seconds) : null;
                }} />
            </div>)}
            <Button variant="ghost" className="alert-condition-remove" aria-label={`Remove ${label}`} title={conditions.length === 1 ? 'Keep at least one condition' : `Remove ${label}`} disabled={conditions.length === 1}
              onClick={() => { setConditions(current => current.filter(item => item.id !== condition.id)); setActive(null); addButton.current?.focus(); }}>×</Button>
          </div>;
        })}</div>
        <div className="alert-condition-add">
          <Button ref={addButton} variant="outline" aria-label="Add condition" aria-haspopup="dialog" aria-expanded={menuOpen} onClick={() => setMenuOpen(!menuOpen)}>＋ Add condition</Button>
          {menuOpen && <div ref={menu} className="alert-condition-menu" role="dialog" aria-label="Add a condition"
            onBlur={event => { if (event.relatedTarget instanceof Node && !event.currentTarget.contains(event.relatedTarget) && !addButton.current?.contains(event.relatedTarget)) setMenuOpen(false); }}
            onKeyDown={event => { if (event.key === 'Escape') { event.preventDefault(); setMenuOpen(false); addButton.current?.focus(); } }}>
            <strong>Add a condition</strong>
            {(['fast', 'sustained', 'custom'] as const).map(preset => {
              const exists = preset !== 'custom' && conditions.some(condition => kind(condition) === preset);
              return <button key={preset} type="button" disabled={exists} onClick={() => addCondition(preset)}><strong>{conditionLabels[preset]}{exists && <span>Added</span>}</strong><small>{conditionDescriptions[preset]}</small></button>;
            })}
            <p>Presets are starting points for a 30-day SLO.</p>
          </div>}
        </div>
        {nonThirtyDayPresets && <div className="alert-preset-confirmation"><label htmlFor={`${prefix}-confirm`}><input id={`${prefix}-confirm`} type="checkbox" checked={confirmed} onChange={event => setConfirmed(event.target.checked)} aria-describedby={errors.confirm ? `${prefix}-confirm-error` : undefined} />Use these 30-day starting presets for this SLO’s {selected.windowSeconds / 86400}-day period.</label>{errors.confirm && <p className="ui-error" id={`${prefix}-confirm-error`}>{errors.confirm}</p>}</div>}
      </FormSection>
      <FormSection step={3} title="Alert destinations"><div className="alert-destination"><div><strong>In Almanac</strong><p>Email, Slack and PagerDuty are planned for a later phase.</p></div><span>Always on</span></div></FormSection>
    </fieldset>
    <FormActions><Button variant="outline" disabled={pending} onClick={onCancel}>Cancel</Button><Button type="submit" pending={pending}>Create alert policy</Button></FormActions>
  </form>;
}
