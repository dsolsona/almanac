import { useState } from 'react';
import { AlertPolicyForm, type AlertSloOption } from '../alerts/AlertPolicyForm.tsx';
import { Button, PageHeader } from '../components/ui/index.ts';

const examples: AlertSloOption[] = [
  { id: 'checkout', label: 'Checkout availability', detail: 'Checkout · 99.9% target · 30 days', target: '0.999', cadenceSeconds: 60, windowSeconds: 2592000 },
  { id: 'payments', label: 'Payment processing for international card transactions', detail: 'Payments · 99.95% target · 7 days', target: '0.9995', cadenceSeconds: 60, windowSeconds: 604800 },
  { id: 'unsupported', label: 'Archived search availability', detail: 'Archived SLO', target: '0.999', cadenceSeconds: 60, windowSeconds: 2592000, disabled: true },
];
export function AlertPolicyExample() {
  const [revision, setRevision] = useState(0), [saved, setSaved] = useState(''), [pending, setPending] = useState(false), [error, setError] = useState(false);
  return <section className="catalogue-section" id="alert-policy" aria-labelledby="alert-policy-example-title">
    <div className="section-heading"><p className="eyebrow mono">ALERTS / FORM</p><h2 id="alert-policy-example-title">Alert policy creation</h2><p>Local presentation example. No policy or alert is saved to Almanac.</p></div>
    <div className="example-row"><Button variant="outline" onClick={() => setPending(!pending)}>{pending ? 'Clear pending example' : 'Show pending example'}</Button><Button variant="outline" onClick={() => setError(!error)}>{error ? 'Clear error example' : 'Show error example'}</Button><Button variant="ghost" onClick={() => { setRevision(value => value + 1); setSaved(''); setPending(false); setError(false); }}>Reset policy example</Button></div>
    <div className="alert-policy-example" style={{ maxWidth: 1112 }}>
      <PageHeader title="New alert policy" summary="Choose an SLO, set the burn conditions and label the urgency." />
      <AlertPolicyForm key={revision} slos={examples} pending={pending} error={error ? 'The policy could not be saved. Your values are preserved.' : undefined}
        onSave={draft => setSaved(`Local draft: ${draft.name}, ${draft.conditions.length} conditions, ${draft.severity} severity. Nothing was saved to Almanac.`)}
        onCancel={() => { setRevision(value => value + 1); setSaved('Local draft discarded.'); }} />
      <p role="status">{saved}</p>
    </div>
  </section>;
}
