import {useCallback, useEffect, useRef, useState, type MouseEvent} from 'react';
import {Button} from './Button.tsx';
import {FilterSelect} from './ListComposition.tsx';
import './resource-table.css';

export type ResourceColumn<T> = {id: string; label: string; value: (item: T) => string | number | null; title?: string};
const collator = new Intl.Collator('en', {numeric: true, sensitivity: 'base'});
type Page<T> = {items: T[]; next_cursor: string};
function accessDenied(failure: unknown) {
  return typeof failure === 'object' && failure !== null
    && (('status' in failure && (failure.status === 401 || failure.status === 403))
      || ('code' in failure && ['unauthenticated', 'unauthorized', 'forbidden'].includes(String(failure.code))));
}

// Fetch complete cursor pages only when sorting is requested. Never present a
// partially collected result as globally sorted, or replay a cyclic cursor.
export function useResourceTable<T extends {id: string}>({items, loadPage, pageSize, enabled = true, scope = ''}: {
  items: T[]; loadPage: (cursor: string, signal: AbortSignal) => Promise<Page<T>>; pageSize: number; enabled?: boolean; scope?: string;
}) {
  const [sort, setSort] = useState({field: '', descending: false});
  const [completed, setCompleted] = useState<{scope: string; items: T[]} | null>(null);
  const snapshot = completed?.scope === scope ? completed.items : null;
  const [rejectedSource, setRejectedSource] = useState<{items: T[]; scope: string} | null>(null);
  const accessRejected = rejectedSource?.items === items;
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [page, setPage] = useState(0);
  const [revision, setRevision] = useState(0);
  const loader = useRef(loadPage); loader.current = loadPage;
  const source = useRef({items, scope}); source.current = {items, scope};
  const inFlight = useRef<{controller: AbortController; deadline: ReturnType<typeof setTimeout>} | null>(null);
  const authority = useRef(0);
  const readGuard = useCallback(() => {const version = authority.current; return () => version === authority.current;}, []);
  const readFailed = useCallback((failure: unknown) => {
    if (!accessDenied(failure)) return false;
    authority.current++;
    if (inFlight.current) {clearTimeout(inFlight.current.deadline); inFlight.current.controller.abort(); inFlight.current = null;}
    setCompleted(null); setRejectedSource(source.current); setBusy(false);
    setError(failure instanceof Error ? failure.message : 'Access to these records was rejected. Check your session and try again.');
    return true;
  }, []);
  const active = !!sort.field && enabled;
  useEffect(() => {
    if (!active) { setCompleted(null); if (!accessRejected) setError(''); setBusy(false); return; }
    const controller = new AbortController();
    const fetchPage = loader.current;
    const deadline = setTimeout(() => {
      controller.abort();
      setError((snapshot ? 'Couldn’t refresh sorted records. Showing the last complete result. ' : '') + 'Sorting took too long. Refresh and try again.');
      setBusy(false);
    }, 30_000);
    inFlight.current = {controller, deadline};
    setBusy(true); setError(''); setPage(0);
    void (async () => {
      const records = new Map<string, T>();
      const seen = new Set<string>();
      let cursor = '';
      do {
        if (seen.has(cursor)) throw new Error('The list changed during sorting. Refresh and try again.');
        seen.add(cursor);
        const result = await fetchPage(cursor, controller.signal);
        if (controller.signal.aborted) return;
        for (const item of result.items) records.set(item.id, item);
        cursor = result.next_cursor;
      } while (cursor);
      setCompleted({scope, items: [...records.values()]});
      setRejectedSource(null);
    })().catch((failure: unknown) => {
      if (!controller.signal.aborted) {
        if (!readFailed(failure)) setError((snapshot ? 'Couldn’t refresh sorted records. Showing the last complete result. ' : '') + (failure instanceof Error ? failure.message : 'The records could not be sorted. Try again.'));
      }
    }).finally(() => {clearTimeout(deadline); if (inFlight.current?.controller === controller) inFlight.current = null; if (!controller.signal.aborted) setBusy(false);});
    return () => {clearTimeout(deadline); controller.abort(); if (inFlight.current?.controller === controller) inFlight.current = null;};
  }, [active, enabled, items, revision, scope, readFailed]);
  const choose = (field: string) => { setSort(current => ({field, descending: current.field === field && !current.descending})); setPage(0); };
  function rows(columns: ResourceColumn<T>[], filter: (item: T) => boolean = () => true) {
    const all = (accessRejected ? [] : active ? snapshot ?? (error ? [] : items) : items).filter(filter);
    const column = columns.find(column => column.id === sort.field);
    if (active && snapshot && column) {
      const keys = new Map(all.map(item => [item.id, column.value(item)]));
      all.sort((a, b) => {
      const left = keys.get(a.id)!, right = keys.get(b.id)!;
      // Missing values stay last in either direction; identity breaks equal keys.
      if (left === null || right === null) return left === right ? a.id.localeCompare(b.id) : left === null ? 1 : -1;
      const comparison = typeof left === 'number' && typeof right === 'number' ? left - right : collator.compare(String(left), String(right));
      return (sort.descending ? -comparison : comparison) || a.id.localeCompare(b.id);
    });
    }
    const boundedPage = Math.min(page, Math.max(0, Math.ceil(all.length / pageSize) - 1));
    const start = active ? boundedPage * pageSize : 0;
    return {items: active ? all.slice(start, start + pageSize) : all, total: all.length, start,
      hasPrevious: boundedPage > 0, hasNext: start + pageSize < all.length};
  }
  return {sort, choose, active, busy, error, readGuard, readFailed, sourceItems: accessRejected ? [] : active ? snapshot ?? (error ? [] : items) : items, rows, next: () => setPage(value => value + 1), previous: () => setPage(value => Math.max(0, value - 1)), resetPage: () => setPage(0), refresh: () => setRevision(value => value + 1)};
}

type Sorting = {busy?: boolean; sort: {field: string; descending: boolean}; choose: (field: string) => void};
export function ResourceTableHead<T>({columns, sorting}: {columns: ResourceColumn<T>[]; sorting: Sorting}) {
  return <thead><tr>{columns.map(column => <th key={column.id} scope="col" aria-sort={!sorting.busy && sorting.sort.field === column.id ? sorting.sort.descending ? 'descending' : 'ascending' : 'none'}><button type="button" className="ui-resource-sort" title={column.title} onClick={() => sorting.choose(column.id)}>{column.label}<span aria-hidden="true">{sorting.sort.field === column.id ? sorting.sort.descending ? '↓' : '↑' : '↕'}</span></button></th>)}</tr></thead>;
}
export function ResourceTableMobileSort<T>({id, columns, sorting}: {id: string; columns: ResourceColumn<T>[]; sorting: Sorting}) {
  return <div className="ui-resource-mobile-sort"><FilterSelect id={id} label="Sort by" value={sorting.sort.field} options={[{value: '', label: 'Created'}, ...columns.map(column => ({value: column.id, label: column.label}))]} onChange={sorting.choose}/><Button variant="outline" disabled={!sorting.sort.field} onClick={() => sorting.choose(sorting.sort.field)}>{sorting.sort.descending ? 'Descending' : 'Ascending'}</Button></div>;
}
export function resourceRowClick(event: MouseEvent<HTMLTableRowElement>, open: () => void) {
  if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey || (event.target as Element).closest('a,button,input,select,textarea,summary,[role="button"]') || window.getSelection()?.toString()) return;
  open();
}
