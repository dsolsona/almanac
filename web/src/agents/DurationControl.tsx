import { useState } from 'react';
import { ChoiceChips } from '../components/ui/index.ts';

// Credential expiry and grace as preset chips with a self-applying "Custom…" chip (ADR-0028,
// approved agent-register mock). Expiry customs are whole days, grace customs whole seconds,
// so a typed value is never rounded (SPEC-0018). The value is seconds as a string: '' keeps the
// default, 'invalid' marks a custom entry the owning form must reject.
type Props = { id: string; label: string; value: string; onChange: (value: string) => void; grace?: boolean; disabled?: boolean };

const expiry = { presets: [['30d', 2592000, '30 days'], ['90d', 7776000, '90 days'], ['1y', 31536000, '1 year']] as const, unit: 86400, max: 365, unitLabel: 'days', fallback: '90d',
  hint: 'Authentication stops when the credential expires. You can rotate it any time from the agent page. Maximum 1 year.' };
const overlap = { presets: [['now', 0, 'Immediately'], ['1m', 60, '1 minute'], ['5m', 300, '5 minutes'], ['10m', 600, '10 minutes']] as const, unit: 1, max: 600, unitLabel: 'seconds', fallback: '10m',
  hint: 'The previous credential remains valid while you update the running agent. Maximum 10 minutes.' };

function choiceFor(seconds: string, spec: typeof expiry | typeof overlap): string {
  if (seconds === '' || seconds === 'invalid') return seconds === '' ? spec.fallback : '';
  const preset = spec.presets.find(([, value]) => String(value) === seconds);
  if (preset) return preset[0];
  const amount = Number(seconds) / spec.unit;
  return Number.isInteger(amount) ? String(amount) : '';
}

export function DurationControl({ id, label, value, onChange, grace = false, disabled }: Props) {
  const spec = grace ? overlap : expiry;
  const [choice, setChoice] = useState(() => choiceFor(value, spec));
  const minimum = grace ? 0 : 1;
  const validate = (text: string) => /^\d+$/.test(text) && Number(text) >= minimum && Number(text) <= spec.max
    ? undefined : `Enter a whole number of ${spec.unitLabel} from ${minimum} to ${spec.max}.`;
  function change(next: string) {
    setChoice(next);
    const preset = spec.presets.find(([key]) => key === next);
    if (preset) onChange(String(preset[1]));
    else onChange(validate(next) === undefined ? String(Number(next) * spec.unit) : 'invalid');
  }
  return <ChoiceChips id={id} label={label} value={choice} onChange={change} disabled={disabled} hint={spec.hint}
    options={spec.presets.map(([key, , text]) => ({ value: key, label: text }))}
    custom={{ unit: spec.unitLabel, placeholder: grace ? 'e.g. 120' : 'e.g. 14', inputMode: 'numeric', validate,
      format: (text) => `${text} ${Number(text) === 1 ? spec.unitLabel.slice(0, -1) : spec.unitLabel}` }} />;
}

export function describeDuration(seconds: number): string {
  if (seconds === 0) return 'Immediately';
  for (const [unit, name] of [[86400, 'day'], [3600, 'hour'], [60, 'minute']] as const) if (seconds % unit === 0) return `${seconds / unit} ${name}${seconds === unit ? '' : 's'}`;
  return `${seconds} second${seconds === 1 ? '' : 's'}`;
}
