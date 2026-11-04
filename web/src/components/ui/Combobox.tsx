import { useEffect, useId, useRef, useState, type ReactNode } from 'react';
import { StatusMark, type MarkTone } from './StatusMark.tsx';
import { SelectionSummary } from './SelectionSummary.tsx';
import './operational-controls.css';

export type ComboboxOption = {
  id: string;
  label: string;
  detail?: string;
  tone?: MarkTone;
  status?: string;
  disabled?: boolean;
  keywords?: string;
  summaryDetail?: ReactNode;
  summaryMessage?: ReactNode;
};

type Props = {
  id: string;
  label: string;
  options: ComboboxOption[];
  value: string | null;
  onChange: (id: string | null) => void;
  placeholder?: string;
  hint?: string;
  error?: string;
  disabled?: boolean;
  badge?: (option: ComboboxOption) => string | undefined;
  noMatch?: { text: (term: string) => string; action?: { label: (term: string) => string; onSelect: (term: string) => void } };
};

function highlight(text: string, term: string): ReactNode {
  if (!term) return text;
  const index = text.toLowerCase().indexOf(term.toLowerCase());
  if (index < 0) return text;
  return <>{text.slice(0, index)}<mark>{text.slice(index, index + term.length)}</mark>{text.slice(index + term.length)}</>;
}

// Type-ahead picker. Options show their state before selection; the chosen option
// collapses to a summary card with a Change action that returns focus to the input.
export function Combobox({ id, label, options, value, onChange, placeholder, hint, error, disabled, badge, noMatch }: Props) {
  const [term, setTerm] = useState('');
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState(-1);
  const [restoreFocus, setRestoreFocus] = useState(false);
  const input = useRef<HTMLInputElement>(null);
  const listId = `${id}-listbox`;
  const generated = useId();
  const selected = value === null ? undefined : options.find((option) => option.id === value);
  const matches = options.filter((option) => !term || `${option.label} ${option.detail ?? ''} ${option.keywords ?? ''}`.toLowerCase().includes(term.toLowerCase()));
  const hintId = hint ? `${id}-hint` : undefined;
  const errorId = error ? `${id}-error` : undefined;
  const describedBy = [hintId, errorId].filter(Boolean).join(' ') || undefined;
  const optionId = (option: ComboboxOption) => `${id}-option-${option.id}`;

  useEffect(() => { if (restoreFocus && !selected) { input.current?.focus(); setRestoreFocus(false); } }, [restoreFocus, selected]);
  useEffect(() => { if (active >= matches.length) setActive(matches.length - 1); }, [matches.length, active]);

  function choose(option: ComboboxOption) {
    if (option.disabled) return;
    onChange(option.id);
    setTerm('');
    setOpen(false);
    setActive(-1);
  }
  function change() {
    onChange(null);
    setRestoreFocus(true);
  }
  function step(delta: number) {
    if (matches.length === 0) return;
    let next = active;
    for (let i = 0; i < matches.length; i++) {
      next = (next + delta + matches.length) % matches.length;
      if (!matches[next]!.disabled) break;
    }
    setActive(next);
  }

  if (selected) {
    return <div className="ui-field">
      <span className="ui-label" id={`${id}-label`}>{label}</span>
      <SelectionSummary
        tone={selected.tone ?? 'none'}
        status={selected.status ?? 'Selected'}
        title={selected.label}
        badge={badge?.(selected)}
        detail={selected.summaryDetail ?? selected.detail}
        message={selected.summaryMessage}
        variant={selected.tone === 'warning' || selected.tone === 'stale' ? 'warning' : 'default'}
        action={disabled ? undefined : { label: 'Change', onClick: change }}
      />
      {hint && <p className="ui-hint" id={hintId}>{hint}</p>}
    </div>;
  }

  return <div className="ui-field">
    <label className="ui-label" htmlFor={id}>{label}</label>
    <div className="ui-combobox">
      <input
        ref={input}
        id={id}
        role="combobox"
        aria-expanded={open}
        aria-controls={listId}
        aria-autocomplete="list"
        aria-activedescendant={open && active >= 0 && matches[active] ? optionId(matches[active]) : undefined}
        aria-invalid={error ? true : undefined}
        aria-describedby={describedBy}
        autoComplete="off"
        className={`ui-control ui-combobox__input ${error ? 'ui-control--invalid' : ''}`.trim()}
        placeholder={placeholder}
        value={term}
        disabled={disabled}
        onFocus={() => setOpen(true)}
        onBlur={() => { setOpen(false); setActive(-1); }}
        onChange={(event) => { setTerm(event.target.value); setOpen(true); setActive(-1); }}
        onKeyDown={(event) => {
          if (event.key === 'ArrowDown') { event.preventDefault(); if (!open) setOpen(true); step(1); }
          else if (event.key === 'ArrowUp') { event.preventDefault(); step(-1); }
          else if (event.key === 'Home' && open) { event.preventDefault(); setActive(matches.findIndex((option) => !option.disabled)); }
          else if (event.key === 'End' && open) { event.preventDefault(); setActive(matches.length - 1); }
          else if (event.key === 'Enter') {
            if (open && active >= 0 && matches[active]) { event.preventDefault(); choose(matches[active]); }
            else if (open && matches.length === 0 && term && noMatch?.action) { event.preventDefault(); noMatch.action.onSelect(term); setTerm(''); setOpen(false); }
          }
          else if (event.key === 'Escape') { event.preventDefault(); setOpen(false); setActive(-1); }
        }}
      />
      <span className="ui-combobox__icon" aria-hidden="true">⌕</span>
      <ul id={listId} role="listbox" aria-labelledby={`${id}`} className="ui-combobox__list" hidden={!open}>
        {matches.map((option, index) => <li
          key={option.id}
          id={optionId(option)}
          role="option"
          aria-selected={index === active}
          aria-disabled={option.disabled || undefined}
          className={`ui-combobox__option ${option.disabled ? 'is-disabled' : ''}`.trim()}
          onMouseDown={(event) => { event.preventDefault(); choose(option); }}
          onMouseEnter={() => { if (!option.disabled) setActive(index); }}
        >
          <StatusMark tone={option.tone ?? 'none'} label="" className="ui-combobox__mark" />
          <span className="ui-combobox__label">{highlight(option.label, term)}</span>
          {option.status && <span className="ui-combobox__status">{option.status}</span>}
          {option.detail && <span className="ui-combobox__detail">{option.detail}</span>}
        </li>)}
        {matches.length === 0 && <li className="ui-combobox__none" id={`${generated}-none`}>
          <span>{noMatch?.text(term) ?? `No match for “${term}”.`}</span>
          {noMatch?.action && term && <button type="button" className="ui-combobox__action" onMouseDown={(event) => { event.preventDefault(); noMatch.action!.onSelect(term); setTerm(''); setOpen(false); }}>{noMatch.action.label(term)}</button>}
        </li>}
      </ul>
    </div>
    {hint && <p className="ui-hint" id={hintId}>{hint}</p>}
    {error && <p className="ui-error" id={errorId}>{error}</p>}
  </div>;
}
