import { useState } from 'react';
import { Button, ChoiceChips, Combobox, Pager, PageHeader, Panel, SearchField, SegmentedControl, SelectionSummary, StatusMark, TextareaField, Toolbar, type ComboboxOption } from '../components/ui/index.ts';

// Local catalogue data only; never application records.
const datasourceExamples: ComboboxOption[] = [
  { id: 'local', label: 'local-victoriametrics', tone: 'healthy', status: 'Healthy', detail: 'Prometheus · 60 s cadence · agent Local · connected', keywords: 'prometheus local', summaryDetail: 'Prometheus · every 60 s · agent Local connected · 9 SLIs collect here' },
  { id: 'eu', label: 'prod-prometheus-eu', tone: 'healthy', status: 'Healthy', detail: 'Prometheus · 30 s cadence · agent eu-west-1 · connected', keywords: 'prometheus eu-west-1' },
  { id: 'us', label: 'prod-prometheus-us', tone: 'stale', status: 'Agent unreachable', detail: 'Prometheus · 30 s cadence · agent us-east-1 · unreachable for 2 h', keywords: 'prometheus us-east-1', summaryMessage: 'You can still create the SLI. The first result arrives when the agent is back; nothing is lost in between.' },
  { id: 'staging', label: 'staging-mimir', tone: 'none', status: 'No agent', detail: 'Prometheus · 60 s cadence · no agent assigned', disabled: true },
];

export function OperationalControlsSection() {
  const [filter, setFilter] = useState('all');
  const [target, setTarget] = useState('99.9');
  const [windowDays, setWindowDays] = useState('30');
  const [datasource, setDatasource] = useState<string | null>(null);
  const [created, setCreated] = useState('');
  const [pageSize, setPageSize] = useState(20);
  const [description, setDescription] = useState('');
  const percent = (value: string) => `${value}%`;
  const validateTarget = (value: string) => /^\d+(?:\.\d{1,5})?$/.test(value) && Number(value) > 0 && Number(value) < 100 ? undefined : 'Target must be above 0% and below 100%.';
  const validateDays = (value: string) => /^\d+$/.test(value) && Number(value) >= 1 && Number(value) <= 365 ? undefined : 'Enter whole days from 1 to 365.';

  return <section className="catalogue-section" id="operational" aria-labelledby="operational-heading">
    <div className="section-heading"><p className="eyebrow mono">08 / OPERATIONAL</p><h2 id="operational-heading">Operational controls</h2><p>Status marks, segmented filters, preset chips, type-ahead pickers and the shared list composition from ADR-0028. Every example keeps its state in this page.</p></div>

    <Panel heading="Page header, search and pager">
      <div className="operational-list-demo">
        <PageHeader title="SLOs" summary="10 SLOs · 4 need attention · all times UTC" updatedAt="10:00:20 UTC" actions={<><Button variant="outline">↻ Refresh</Button><Button>New SLO</Button></>} />
        <Toolbar>
          <SearchField id="catalogue-search" label="Search SLOs" placeholder="Search by name, service or SLI" shortcut />
          <SegmentedControl label="Filter by status" value={filter} onChange={setFilter} options={[{ value: 'all', label: 'All', count: 10 }, { value: 'attention', label: 'Needs attention', count: 4 }, { value: 'healthy', label: 'Healthy', count: 4 }, { value: 'archived', label: 'Archived', disabled: true, title: 'Not available yet' }]} />
        </Toolbar>
        <p className="demo-feedback" role="status">Filter: {filter}.</p>
        <Pager id="catalogue-page-size" label="Example result pages" position="Page 1 · showing 10 of 10" pageSize={pageSize} onPageSizeChange={setPageSize} hasPrevious={false} hasNext={false} />
      </div>
    </Panel>

    <div className="form-grid">
      <Panel heading="Status marks">
        <div className="operational-marks">
          <StatusMark tone="healthy" label="Healthy" reason="on track" />
          <StatusMark tone="warning" label="Burning fast" reason="1 h burn 18× · 59% left" />
          <StatusMark tone="breached" label="Breached" reason="window below target" />
          <StatusMark tone="stale" label="Not reporting" reason="last sample 26 h ago" />
          <StatusMark tone="none" label="No data yet" reason="first interval pending" />
        </div>
        <p className="demo-feedback">The shape carries the tone; the reason line explains it in one mono line.</p>
      </Panel>
      <Panel heading="Preset chips with a custom value">
        <div className="field-stack">
          <ChoiceChips id="catalogue-target" label="Target" value={target} onChange={setTarget} options={['99', '99.5', '99.9', '99.95', '99.99'].map((value) => ({ value, label: `${value}%` }))} custom={{ unit: '%', placeholder: 'e.g. 99.7', format: percent, validate: validateTarget }} hint={`Error budget is the rest: ${(100 - Number(target)).toFixed(Math.min(5, Math.max(1, (target.split('.')[1] ?? '').length)))}%.`} />
          <ChoiceChips id="catalogue-window-disabled" label="Rolling window (disabled)" value="30" onChange={() => undefined} disabled options={[['7', '7 days'], ['30', '30 days']].map(([value, label]) => ({ value: value!, label: label! }))} custom={{ unit: 'days', format: (value) => `${value} days` }} hint="A disabled group keeps its selection visible." />
          <ChoiceChips id="catalogue-window" label="Rolling window" value={windowDays} onChange={setWindowDays} options={[['7', '7 days'], ['28', '28 days'], ['30', '30 days'], ['90', '90 days']].map(([value, label]) => ({ value: value!, label: label! }))} custom={{ unit: 'days', placeholder: 'e.g. 14', format: (value) => `${value} days`, validate: validateDays, inputMode: 'numeric' }} hint={`Every evaluation covers the last ${windowDays} days.`} />
        </div>
      </Panel>
    </div>

    <div className="form-grid">
      <Panel heading="Type-ahead picker">
        <div className="field-stack">
          <Combobox id="catalogue-datasource" label="Datasource" value={datasource} onChange={setDatasource} options={datasourceExamples} placeholder="Type a datasource name, provider or agent…" hint="The datasource’s agent runs the queries every minute." badge={() => 'Prometheus'} noMatch={{ text: (term) => `No datasource matches “${term}”.`, action: { label: () => 'Add a datasource', onSelect: (term) => setCreated(`Would open the datasource form prefilled with “${term}”.`) } }} />
          {created && <p className="demo-feedback" role="status">{created}</p>}
          <Combobox id="catalogue-datasource-invalid" label="Datasource (invalid)" value={null} onChange={() => undefined} options={datasourceExamples} placeholder="Type a datasource name…" error="Choose an existing Prometheus datasource." />
          <Combobox id="catalogue-datasource-disabled" label="Datasource (disabled)" value="local" onChange={() => undefined} options={datasourceExamples} disabled hint="A disabled picker shows its selection without a Change action." />
        </div>
      </Panel>
      <Panel heading="Selection summary and textarea">
        <div className="field-stack">
          <SelectionSummary tone="none" status="New service" title="Fulfilment" badge="fulfilment" detail="Will be created with this SLO · you can rename it later from the service page" action={{ label: 'Change', onClick: () => undefined }} />
          <SelectionSummary tone="warning" status="Agent unreachable" title="prod-prometheus-us" badge="Prometheus" variant="warning" detail="30 s intervals · agent us-east-1 unreachable for 2 h" message="You can still create the SLI. The first result arrives when the agent is back." action={{ label: 'Change', onClick: () => undefined }} />
          <TextareaField id="catalogue-description" label="Description" optional rows={2} value={description} onChange={(event) => setDescription(event.target.value)} placeholder="What counts as “good”? One line is enough." hint="The first line appears in lists; the rest on the detail page." />
          <TextareaField id="catalogue-description-invalid" label="Description (invalid)" rows={2} value="x" onChange={() => undefined} error="Keep the description under 2,000 characters." />
        </div>
      </Panel>
    </div>
  </section>;
}
