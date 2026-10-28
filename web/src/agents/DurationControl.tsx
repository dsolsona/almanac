import { useState } from 'react';
import { SelectField, TextField } from '../components/ui/index.ts';

type Props = { id: string; label: string; value: string; onChange: (value: string) => void; grace?: boolean; disabled?: boolean };
export function DurationControl({ id, label, value, onChange, grace = false, disabled }: Props) {
  const presets = grace ? [['0', 'Immediately'], ['60', '1 minute'], ['300', '5 minutes'], ['600', '10 minutes']] : [['86400', '1 day'], ['2592000', '30 days'], ['7776000', '90 days'], ['31536000', '1 year']];
  const [custom, setCustom] = useState(Boolean(value) && !presets.some(([seconds]) => seconds === value));
  const [amount, setAmount] = useState(value && !presets.some(([seconds]) => seconds === value) ? value : '');
  const [unit, setUnit] = useState('1');
  function update(number: string, factor: string) {
    setAmount(number); setUnit(factor);
    const seconds = Number(number) * Number(factor);
    onChange(number.trim() && Number.isSafeInteger(seconds) ? String(seconds) : 'invalid');
  }
  return <div className="agents-duration"><SelectField id={id} label={label} value={custom ? 'custom' : value || (grace ? '600' : '7776000')} disabled={disabled} hint={grace ? 'The previous credential remains valid while you update the running agent. Maximum 10 minutes.' : 'Authentication stops when the credential expires. Maximum 1 year.'} options={[...presets.map(([seconds, text]) => ({ value: seconds, label: text })), { value: 'custom', label: 'Custom' }]} onChange={(event) => { if (event.target.value === 'custom') { setCustom(true); update('', unit); } else { setCustom(false); onChange(event.target.value); } }} />
    {custom && <><TextField id={`${id}-amount`} label="Custom amount" type="number" min="0" value={amount} disabled={disabled} onChange={(event) => update(event.target.value, unit)} /><SelectField id={`${id}-unit`} label="Unit" value={unit} disabled={disabled} onChange={(event) => update(amount, event.target.value)} options={[{ value: '1', label: 'Seconds' }, { value: '60', label: 'Minutes' }, ...(!grace ? [{ value: '3600', label: 'Hours' }, { value: '86400', label: 'Days' }] : [])]} /></>}
  </div>;
}
export function describeDuration(seconds: number): string {
  if (seconds === 0) return 'Immediately';
  for (const [unit, name] of [[86400, 'day'], [3600, 'hour'], [60, 'minute']] as const) if (seconds % unit === 0) return `${seconds / unit} ${name}${seconds === unit ? '' : 's'}`;
  return `${seconds} second${seconds === 1 ? '' : 's'}`;
}
