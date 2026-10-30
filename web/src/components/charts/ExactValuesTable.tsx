import { useId } from 'react';
import { Button } from '../ui/Button.tsx';
import type { Coverage } from './timelineModel.ts';
import './charts.css';

export type ExactValueRow = { timestamp: string; lane: string; series: string; unit: string; value: string | null; coverage: Coverage; details?: string };
export function ExactValuesTable({ rows, page, totalRows, onPageChange }: {
  rows: ExactValueRow[]; page: number; totalRows: number; onPageChange: (page: number) => void;
}) {
  const titleId = useId();
  const validTotal = Number.isInteger(totalRows) && totalRows >= 0;
  const pageCount = validTotal ? Math.max(1, Math.ceil(totalRows / 50)) : 1;
  const error = rows.length > 50 ? 'Exact-value pages cannot contain more than 50 rows.'
    : !validTotal ? 'Total row count must be a non-negative integer.'
      : !Number.isInteger(page) || page < 1 || page > pageCount ? `Page must be between 1 and ${pageCount}.`
        : rows.length !== (totalRows === 0 ? 0 : Math.min(50, totalRows - (page - 1) * 50))
          ? `Expected ${totalRows === 0 ? 0 : Math.min(50, totalRows - (page - 1) * 50)} rows for page ${page}, received ${rows.length}.`
          : null;
  if (error) return <section className="exact-values" aria-labelledby={titleId}><h2 id={titleId}>Exact values</h2><p role="alert">Table unavailable: {error}</p></section>;
  const first = totalRows === 0 ? 0 : (page - 1) * 50 + 1;
  const last = Math.min(page * 50, totalRows);
  const hasDetails = rows.some(row => row.details !== undefined);
  return <section className="exact-values" aria-labelledby={titleId}>
    <h2 id={titleId}>Exact values</h2>
    <div className="exact-values__scroll" role="region" aria-label="Exact timeline values" tabIndex={0}>
      <table>
        <caption>Exact timeline values for the displayed range</caption>
        <thead><tr><th scope="col">Timestamp (UTC)</th><th scope="col">Lane</th><th scope="col">Series</th><th scope="col">Value</th><th scope="col">Coverage</th>{hasDetails&&<th scope="col">Details</th>}</tr></thead>
        <tbody>{rows.map((row, index) => <tr key={`${row.timestamp}-${row.lane}-${row.series}-${index}`}>
          <td className="mono">{row.timestamp}</td><td>{row.lane}</td><td>{row.series}</td>
          <td className="mono">{row.value === null ? '—' : `${row.value} ${row.unit}`}</td>
          <td><span className={`coverage-label coverage-label--${row.coverage}`}>{({ 'zero-traffic': 'Zero traffic', missing: 'Missing', partial: 'Partial', complete: 'Complete', 'known-empty': 'Known empty', expired: 'Expired history', 'not-applicable': 'Not applicable', stale: 'Stale' })[row.coverage]}</span></td>{hasDetails&&<td>{row.details??'—'}</td>}
        </tr>)}</tbody>
      </table>
    </div>
    <div className="exact-values__controls"><span className="mono">{first}–{last} of {totalRows} · page {page} of {pageCount}</span>
      <div><Button variant="outline" disabled={page <= 1} onClick={() => onPageChange(page - 1)}>Previous</Button><Button variant="outline" disabled={page >= pageCount} onClick={() => onPageChange(page + 1)}>Next</Button></div>
    </div>
  </section>;
}
