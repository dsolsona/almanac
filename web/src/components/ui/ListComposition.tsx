import { useEffect, useRef, type InputHTMLAttributes, type ReactNode, type RefObject } from 'react';
import { Button } from './Button.tsx';
import { SelectField } from './SelectField.tsx';
import './operational-controls.css';

type PageHeaderProps = {
  title: string;
  // An intro sentence for forms only; lists carry no count subline (ADR-0031).
  summary?: ReactNode;
  breadcrumb?: { href: string; label: string; current: string };
  actions?: ReactNode;
  headingRef?: RefObject<HTMLHeadingElement | null>;
};

// Page title row: Bevan title with the page's actions on the same line. Lists put their one
// primary action here and their refresh at the end of the toolbar; there is no "Updated" line.
export function PageHeader({ title, summary, breadcrumb, actions, headingRef }: PageHeaderProps) {
  return <div className="ui-page-head">
    <div className="ui-page-head__copy">
      {breadcrumb && <nav className="app-breadcrumbs" aria-label="Breadcrumb"><a href={breadcrumb.href} data-app-route>{breadcrumb.label}</a><span aria-hidden="true">/</span><span aria-current="page">{breadcrumb.current}</span></nav>}
      <h1 ref={headingRef} className="app-page-heading ui-page-head__title" tabIndex={-1}>{title}</h1>
      {summary && <p className="ui-page-head__summary">{summary}</p>}
    </div>
    {actions && <div className="ui-page-head__actions">{actions}</div>}
  </div>;
}

// Quiet icon button for refreshing a list or record. Its tooltip carries the last refresh time,
// which replaces the "Updated hh:mm:ss" line; failures are reported by the page's own notice.
export function RefreshButton({ label, onClick, busy = false, disabled = false, lastRefreshed }: { label: string; onClick: () => void; busy?: boolean; disabled?: boolean; lastRefreshed?: string }) {
  return <Button variant="ghost" className="ui-refresh" aria-label={label} title={lastRefreshed ? `${label} · last refreshed ${lastRefreshed} UTC` : label} pending={busy} disabled={disabled} onClick={onClick}>
    {!busy && <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M20 11a8 8 0 1 0-2.3 5.7" /><path d="M20 4v7h-7" /></svg>}
  </Button>;
}

// A secondary list filter that sits in the toolbar row at control height, label inside.
export function FilterSelect({ id, label, value, options, onChange, disabled }: { id: string; label: string; value: string; options: { value: string; label: string }[]; onChange: (value: string) => void; disabled?: boolean }) {
  return <label className={`ui-filter ${value ? 'ui-filter--on' : ''}`.trim()} htmlFor={id}>
    <span className="ui-filter__label" aria-hidden="true">{label}</span>
    <select id={id} aria-label={label} value={value} disabled={disabled} onChange={(event) => onChange(event.target.value)}>{options.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}</select>
  </label>;
}

export function ToolbarSpacer() { return <span className="ui-toolbar__spacer" aria-hidden="true" />; }

type SearchFieldProps = Omit<InputHTMLAttributes<HTMLInputElement>, 'type'> & {
  id: string;
  label: string;
  shortcut?: boolean;
  inputRef?: RefObject<HTMLInputElement | null>;
};

// Search input with an icon, a visually hidden label and an optional "/" focus shortcut.
export function SearchField({ id, label, shortcut = false, inputRef, className = '', ...props }: SearchFieldProps) {
  const localRef = useRef<HTMLInputElement>(null);
  const ref = inputRef ?? localRef;
  useEffect(() => {
    if (!shortcut) return;
    const focusSearch = (event: KeyboardEvent) => {
      if (event.key !== '/' || event.metaKey || event.ctrlKey || event.altKey) return;
      if (event.target instanceof HTMLElement && event.target.closest('input, textarea, select, [contenteditable="true"]')) return;
      event.preventDefault();
      ref.current?.focus();
    };
    window.addEventListener('keydown', focusSearch);
    return () => window.removeEventListener('keydown', focusSearch);
  }, [shortcut, ref]);
  return <label className={`ui-search ${className}`.trim()} htmlFor={id}>
    <svg viewBox="0 0 20 20" aria-hidden="true"><circle cx="8" cy="8" r="5.5" fill="none" stroke="currentColor" strokeWidth="1.5"/><path d="m12 12 5 5" stroke="currentColor" strokeWidth="1.5"/></svg>
    <input {...props} ref={ref} id={id} type="search" autoComplete="off" aria-label={label} />
    {shortcut && <kbd aria-hidden="true">/</kbd>}
  </label>;
}

export function Toolbar({ children, className = '' }: { children: ReactNode; className?: string }) {
  return <div className={`ui-toolbar ${className}`.trim()}>{children}</div>;
}

type PagerProps = {
  id: string;
  label: string;
  position: string;
  pageSize: number;
  pageSizes?: number[];
  onPageSizeChange: (size: number) => void;
  onPrevious?: () => void;
  onNext?: () => void;
  hasPrevious: boolean;
  hasNext: boolean;
  busy?: boolean;
};

// Position, per-page choice and previous/next, always visible so the list's extent is known.
export function Pager({ id, label, position, pageSize, pageSizes = [20, 10], onPageSizeChange, onPrevious, onNext, hasPrevious, hasNext, busy = false }: PagerProps) {
  return <nav className="ui-pager" aria-label={label}>
    <span className="ui-pager__position">{position}</span>
    <div className="ui-pager__size"><SelectField id={id} label="Per page" value={String(pageSize)} options={pageSizes.map((size) => ({ value: String(size), label: String(size) }))} onChange={(event) => onPageSizeChange(Number(event.target.value))} /></div>
    <span className="ui-pager__steps">
      <Button variant="outline" className="ui-pager__step" disabled={!hasPrevious || busy} onClick={onPrevious}>‹ Previous</Button>
      <Button variant="outline" className="ui-pager__step" disabled={!hasNext || busy} onClick={onNext}>Next ›</Button>
    </span>
  </nav>;
}
