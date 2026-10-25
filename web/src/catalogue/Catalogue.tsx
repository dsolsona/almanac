import { lazy, Suspense, useRef, useState } from 'react';
import { Button, DatePicker, Dialog, LoadingIndicator, Panel, SelectField, StatusBadge, Switch, TextField, ThemeToggle } from '../components/ui/index.ts';
import '../styles/catalogue.css';

const ChartsSection = lazy(() => import('./ChartsSection.tsx').then((module) => ({ default: module.ChartsSection })));

const colors = [
  ['Paper', '--almanac-paper'], ['Ink', '--almanac-ink'], ['Vermilion', '--almanac-vermilion'], ['Teal', '--almanac-teal'],
  ['Background', '--bg'], ['Surface', '--surface'], ['Secondary surface', '--surface-2'], ['Border', '--border'],
  ['Healthy', '--status-healthy'], ['Warning', '--status-warning'], ['Breached', '--status-breached'], ['No data', '--status-no-data'],
];

export function Catalogue() {
  const [dialogOpen, setDialogOpen] = useState(false);
  const [pending, setPending] = useState(false);
  const [count, setCount] = useState(0);
  const [feedback, setFeedback] = useState('No local action yet.');
  const [demoDate, setDemoDate] = useState('2026-10-25');
  const [chartsOpen, setChartsOpen] = useState(false);
  const triggerRef = useRef<HTMLButtonElement>(null);
  function demonstrateSave() {
    setPending(true);
    setFeedback('Saving this local example…');
  }
  function finishSave() {
    setPending(false);
    setCount((value) => value + 1);
    setFeedback('Saved locally for this page only.');
  }

  return <>
    <a className="skip-link" href="#catalogue-main">Skip to catalogue</a>
    <header className="masthead"><a href="/" className="wordmark">ALMANAC</a><span className="catalogue-label">LOCAL COMPONENT CATALOGUE</span><ThemeToggle /></header>
    <nav className="catalogue-nav" aria-label="Catalogue sections">
      <a href="#foundations">Foundations</a><a href="#buttons">Buttons</a><a href="#forms">Forms</a><a href="#status">Status</a><a href="#panels">Panels</a><a href="#dialog">Dialog</a><a href="#controls">Controls</a><button type="button" aria-expanded={chartsOpen} aria-controls={chartsOpen ? 'charts' : undefined} onClick={() => setChartsOpen(true)}>Charts</button>
    </nav>
    <main id="catalogue-main" className="catalogue-main">
      <section className="catalogue-intro">
        <p className="eyebrow mono">DESIGN SYSTEM · LOCAL EXAMPLES</p>
        <h1>Clear signals.<br />Steady decisions.</h1>
        <p>This page demonstrates Almanac’s shared visual foundations and reusable controls. Every action stays in this browser tab; examples do not connect to application data or services.</p>
      </section>

      <section className="catalogue-section" id="foundations" aria-labelledby="foundations-heading">
        <div className="section-heading"><p className="eyebrow mono">01 / FOUNDATION</p><h2 id="foundations-heading">Color and type</h2><p>Semantic tokens keep page surfaces, actions and operational meaning consistent in both themes.</p></div>
        <div className="foundation-grid">
          <Panel heading="Palette"><div className="swatch-grid">{colors.map(([label, token]) => <div className="swatch" key={token}><span className="swatch__sample" style={{ background: `var(${token})` }} aria-hidden="true"/><span>{label}</span><code>{token}</code></div>)}</div></Panel>
          <Panel heading="Typography"><div className="type-specimens"><p className="wordmark type-wordmark">ALMANAC</p><h3 className="type-heading">Paper, ink and signal</h3><p>System sans keeps reading comfortable, including dense operational labels and descriptions.</p><p className="mono">AGENT-07 · 99.95% · 1,482 ms</p><p className="numeric">1234567890 <span>tabular figures</span></p></div></Panel>
        </div>
      </section>

      <section className="catalogue-section" id="buttons" aria-labelledby="buttons-heading">
        <div className="section-heading"><p className="eyebrow mono">02 / INTERACTION</p><h2 id="buttons-heading">Buttons</h2><p>Native button semantics, clear variants and a pending state that blocks repeat actions.</p></div>
        <Panel heading="Variants and states"><div className="example-row"><Button onClick={() => { setCount((value) => value + 1); setFeedback('Button action updated local count.'); }}>Primary</Button><Button variant="outline" onClick={() => setFeedback('Outline action is local only.')}>Outline</Button><Button variant="ghost" onClick={() => setFeedback('Ghost action is local only.')}>Ghost</Button><Button variant="danger" onClick={() => setFeedback('Danger example did not change application data.')}>Danger</Button><Button disabled>Disabled</Button><Button pending>Pending</Button><Button onClick={demonstrateSave} pending={pending}>{pending ? 'Saving locally' : 'Save local example'}</Button>{pending && <Button variant="outline" onClick={finishSave}>Finish local example</Button>}</div><p className="demo-feedback" role="status">{feedback} Count: {count}.</p></Panel>
      </section>

      <section className="catalogue-section" id="forms" aria-labelledby="forms-heading">
        <div className="section-heading"><p className="eyebrow mono">03 / INPUT</p><h2 id="forms-heading">Form controls</h2><p>Labels stay visible; hints and errors are linked to their controls for assistive technology.</p></div>
        <div className="form-grid">
          <Panel heading="Text fields"><div className="field-stack"><TextField id="catalogue-name" label="Datasource name" hint="A short name used in navigation." placeholder="Production metrics"/><TextField id="catalogue-invalid" label="Query interval" defaultValue="not a duration" error="Enter a duration such as 30s or 5m."/><TextField id="catalogue-disabled" label="Read-only identifier" value="datasource_01HZXK8T5D9W" disabled onChange={() => undefined}/><TextField id="catalogue-long" label="Long label example — preserve the complete wording at narrow widths" hint="Descriptions can wrap onto additional lines without clipping." placeholder="Type a longer description"/></div></Panel>
          <Panel heading="Native select"><div className="field-stack"><SelectField id="catalogue-range" label="Evaluation range" options={[{ value: '', label: 'Choose a range' }, { value: '6h', label: 'Last 6 hours' }, { value: '24h', label: 'Last 24 hours' }, { value: 'custom', label: 'Custom range', disabled: true }]} hint="Choose a local example range."/><SelectField id="catalogue-error-select" label="Data source" options={[{ value: '', label: 'Select a source' }]} error="Choose a source before continuing."/></div></Panel>
        </div>
      </section>

      <section className="catalogue-section" id="status" aria-labelledby="status-heading">
        <div className="section-heading"><p className="eyebrow mono">04 / MEANING</p><h2 id="status-heading">Operational status</h2><p>Status uses text, distinct marks and semantic color. Vermilion remains a brand action color.</p></div>
        <Panel heading="Status examples"><div className="status-grid"><StatusBadge status="healthy"/><StatusBadge status="warning"/><StatusBadge status="breached"/><StatusBadge status="no-data"/><StatusBadge status="stale"/></div></Panel>
      </section>

      <section className="catalogue-section" id="panels" aria-labelledby="panels-heading">
        <div className="section-heading"><p className="eyebrow mono">05 / GROUPING</p><h2 id="panels-heading">Panels and page states</h2><p>One restrained border groups related controls without nested decorative cards.</p></div>
        <div className="panel-grid"><Panel heading="Empty state"><p>No SLOs are available in this local example.</p><Button variant="outline" onClick={() => setFeedback('Empty-state action is local only.')}>Review filters</Button></Panel><Panel heading="Loading state"><LoadingIndicator label="Loading local example"/></Panel><Panel heading="Error state"><p className="ui-error" role="alert">This local example could not be refreshed.</p><Button variant="outline" onClick={() => setFeedback('Retry completed locally; no service was contacted.')}>Try local retry</Button></Panel></div>
        <Panel heading="Long content remains readable"><p className="long-copy">A sustained period of incomplete observations can make an otherwise familiar objective difficult to interpret. Almanac keeps explanatory language close to the value it describes, allows prose to wrap at narrow viewport widths, and preserves the distinction between unavailable, stale and healthy information. This paragraph is intentionally long enough to show the catalogue’s reading measure and responsive behavior without adding page-wide horizontal scrolling.</p></Panel>
      </section>

      <section className="catalogue-section" id="dialog" aria-labelledby="dialog-heading">
        <div className="section-heading"><p className="eyebrow mono">06 / OVERLAY</p><h2 id="dialog-heading">Dialog</h2><p>Native dialog handles modal focus and Escape; closing returns focus to its trigger.</p></div>
        <Panel heading="Working modal example"><Button ref={triggerRef} onClick={() => setDialogOpen(true)}>Open dialog</Button><Dialog open={dialogOpen} onClose={() => setDialogOpen(false)} title="A local dialog" triggerRef={triggerRef}><p>This is a local example. Press Tab to move among controls and Escape to close.</p><TextField id="dialog-example" label="Example input" hint="Focus remains inside the open dialog."/><div className="example-row dialog-actions"><Button variant="outline" onClick={() => setDialogOpen(false)}>Cancel</Button><Button onClick={() => setDialogOpen(false)}>Done</Button></div></Dialog></Panel>
      </section>
      <section className="catalogue-section" id="controls" aria-labelledby="controls-heading"><div className="section-heading"><p className="eyebrow mono">07 / CONTROLS</p><h2 id="controls-heading">Calendar, switches and loading</h2><p>Keyboard-first controls and visible pending feedback remain responsive at narrow widths.</p></div><div className="control-panels"><Panel heading="Calendar and switches"><div className="feedback-controls"><div className="field-stack"><DatePicker label="Start date" value={demoDate} today="2026-10-25" onValueChange={(date) => { setDemoDate(date); setFeedback(date ? `Calendar selected ${date}.` : 'Calendar selection cleared.'); }} hint="Calendar dates are shown in local form."/><DatePicker label="Locked date" value="2026-10-25" today="2026-10-25" disabled onValueChange={() => undefined}/></div><div className="field-stack"><Switch label="Notifications" hint="Send a local example notification."/><Switch label="Maintenance mode" hint="This is a disabled example." checked disabled onChange={() => undefined}/></div></div></Panel><Panel heading="Loading feedback"><LoadingIndicator label="Refreshing catalogue data"/></Panel></div></section>
      {chartsOpen && <Suspense fallback={<p role="status">Loading chart examples…</p>}><ChartsSection /></Suspense>}
      <footer className="catalogue-footer"><span className="mono">ALMANAC · FOUNDATION CATALOGUE</span><a href="#catalogue-main">Back to top ↑</a></footer>
    </main>
  </>;
}
