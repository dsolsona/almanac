import { useEffect, useRef, type InputHTMLAttributes, type ReactNode, type RefObject } from 'react';
import { Button } from './Button.tsx';
import { SelectField } from './SelectField.tsx';
import './operational-controls.css';

type PageHeaderProps = {
  title: string;
  summary?: ReactNode;
  breadcrumb?: { href: string; label: string; current: string };
  updatedAt?: string;
  actions?: ReactNode;
  headingRef?: RefObject<HTMLHeadingElement | null>;
};

// Page title row: Bevan title, mono summary, and the action group aligned to the title line.
export function PageHeader({ title, summary, breadcrumb, updatedAt, actions, headingRef }: PageHeaderProps) {
  return <div className="ui-page-head">
    <div className="ui-page-head__copy">
      {breadcrumb && <nav className="app-breadcrumbs" aria-label="Breadcrumb"><a href={breadcrumb.href} data-app-route>{breadcrumb.label}</a><span aria-hidden="true">/</span><span aria-current="page">{breadcrumb.current}</span></nav>}
      <h1 ref={headingRef} className="app-page-heading ui-page-head__title" tabIndex={-1}>{title}</h1>
      {summary && <p className="ui-page-head__summary">{summary}</p>}
    </div>
    {(updatedAt || actions) && <div className="ui-page-head__actions">
      {updatedAt && <span className="ui-page-head__updated">Updated {updatedAt}</span>}
      {actions}
    </div>}
  </div>;
}

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
    <span className="sr-only">{label}</span>
    <input {...props} ref={ref} id={id} type="search" autoComplete="off" />
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
