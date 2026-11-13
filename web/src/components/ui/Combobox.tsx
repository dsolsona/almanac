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
  searchLabelOnly?: boolean;
  selectionPresentation?: 'summary' | 'compact';
  multiple?: {selected: ComboboxOption[]; onRemove: (id:string)=>void; removeLabel?: (option:ComboboxOption)=>string};
  // The caller supplies filtered options when handling search.
  onSearch?: (term:string)=>void;
};

function highlight(text: string, term: string): ReactNode {
  if (!term) return text;
  const index = text.toLowerCase().indexOf(term.toLowerCase());
  if (index < 0) return text;
  return <>{text.slice(0, index)}<mark>{text.slice(index, index + term.length)}</mark>{text.slice(index + term.length)}</>;
}

// Type-ahead picker. Options show their state before selection; the chosen option
// collapses to a summary card with a Change action that returns focus to the input.
export function Combobox({ id, label, options, value, onChange, placeholder, hint, error, disabled, badge, noMatch, searchLabelOnly = false, selectionPresentation = 'summary', multiple, onSearch }: Props) {
  const [term, setTerm] = useState('');
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState(-1);
  const [restoreFocus, setRestoreFocus] = useState(false);
  const input = useRef<HTMLInputElement>(null);
  const wrapper = useRef<HTMLDivElement>(null);
  const changeButton = useRef<HTMLButtonElement>(null);
  const [focusChange, setFocusChange] = useState(false);
  const listId = `${id}-listbox`;
  const generated = useId();
  const selected = value === null ? undefined : options.find((option) => option.id === value);
  const matches = options.filter(option=>!multiple?.selected.some(selected=>selected.id===option.id)).filter((option) => onSearch!==undefined || !term || (searchLabelOnly ? option.label : `${option.label} ${option.detail ?? ''} ${option.keywords ?? ''}`).toLowerCase().includes(term.toLowerCase()));
  const hintId = hint ? `${id}-hint` : undefined;
  const errorId = error ? `${id}-error` : undefined;
  const describedBy = [hintId, errorId].filter(Boolean).join(' ') || undefined;
  const optionId = (option: ComboboxOption) => `${id}-option-${option.id}`;

  useEffect(() => { if (restoreFocus && !selected) { input.current?.focus(); setRestoreFocus(false); } }, [restoreFocus, selected]);
  useEffect(() => { if (active >= matches.length) setActive(matches.length - 1); }, [matches.length, active]);
  // Keyboard navigation keeps the highlighted option in view inside the scrolling list.
  useEffect(() => { if (open && active >= 0 && matches[active]) document.getElementById(optionId(matches[active]))?.scrollIntoView?.({ block: 'nearest' }); }, [open, active]); // eslint-disable-line react-hooks/exhaustive-deps
  // After choosing, focus lands on the summary's Change action so keyboard users keep a visible position.
  useEffect(() => { if (focusChange && selected) { changeButton.current?.focus(); setFocusChange(false); } }, [focusChange, selected]);

  function choose(option: ComboboxOption) {
    if (option.disabled) return;
    onChange(option.id);
    setTerm('');
    setOpen(false);
    setActive(-1);
    if(multiple){input.current?.focus();onSearch?.('');setOpen(false);}else setFocusChange(true);
  }
  function runNoMatch() {
    if (!noMatch?.action || !term) return;
    noMatch.action.onSelect(term);
    setTerm('');
    setOpen(false);
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

  if (selected && !multiple) {
    return <div className="ui-field">
      <span className="ui-label" id={`${id}-label`}>{label}</span>
      {selectionPresentation === 'compact' ? <div className={`ui-combobox__selection ui-control ${error ? 'ui-control--invalid' : ''}`} role="group" aria-labelledby={`${id}-label`} aria-describedby={describedBy}>
        <span className="ui-combobox__selected-title" title={selected.label}>{selected.label}</span>
        {!disabled && <button ref={changeButton} id={id} type="button" className="ui-summary__action" aria-label={`Change ${label}`} onClick={change}>Change</button>}
      </div> : <SelectionSummary
        tone={selected.tone ?? 'none'}
        status={selected.status ?? 'Selected'}
        title={selected.label}
        badge={badge?.(selected)}
        detail={selected.summaryDetail ?? selected.detail}
        message={selected.summaryMessage}
        variant={selected.tone === 'warning' || selected.tone === 'stale' ? 'warning' : 'default'}
        action={disabled ? undefined : { label: 'Change', onClick: change, ref: changeButton }}
      />}
      {hint && <p className="ui-hint" id={hintId}>{hint}</p>}
      {error && <p className="ui-error" id={errorId} role="alert">{error}</p>}
    </div>;
  }

  return <div className="ui-field">
    <label className="ui-label" htmlFor={id}>{label}</label>
    <div className="ui-combobox" ref={wrapper} onBlur={(event) => { if (event.relatedTarget instanceof Node && wrapper.current?.contains(event.relatedTarget)) return; setOpen(false); setActive(-1); }}>
      <div className={multiple?'ui-combobox__tokens':'ui-combobox__control'}>
      {multiple&&multiple.selected.length>0&&<ul className="ui-input-tokens" aria-label="Selected dependencies">{multiple.selected.map(option=><li className="ui-input-token" key={option.id}><span>{option.label}</span><button type="button" disabled={disabled} aria-label={multiple.removeLabel?.(option)??`Remove ${option.label}`} onClick={()=>{multiple.onRemove(option.id);input.current?.focus();setOpen(false);}}><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" aria-hidden="true"><path d="m6 6 12 12M18 6 6 18"/></svg></button></li>)}</ul>}
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
        onChange={(event) => { setTerm(event.target.value); onSearch?.(event.target.value); setOpen(true); setActive(-1); }}
        onKeyDown={(event) => {
          if (event.key === 'ArrowDown') { event.preventDefault(); if (!open) setOpen(true); step(1); }
          else if (event.key === 'ArrowUp') { event.preventDefault(); step(-1); }
          else if (event.key === 'Home' && open) { event.preventDefault(); setActive(matches.findIndex((option) => !option.disabled)); }
          else if (event.key === 'End' && open) { event.preventDefault(); setActive(matches.reduce((last, option, index) => option.disabled ? last : index, -1)); }
          else if (event.key === 'Enter') {
            if (open && active >= 0 && matches[active]) { event.preventDefault(); choose(matches[active]); }
            else if(multiple&&open&&matches.length===1&&!matches[0]!.disabled){event.preventDefault();choose(matches[0]!);}
            else if (open && matches.length === 0 && term && noMatch?.action) { event.preventDefault(); runNoMatch(); }
          }
          else if (event.key === 'Escape') { event.preventDefault(); if(multiple)event.stopPropagation(); setOpen(false); setActive(-1); }
        }}
      />
      </div>
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
          {noMatch?.action && term && <button type="button" className="ui-combobox__action" onMouseDown={(event) => event.preventDefault()} onClick={runNoMatch}>{noMatch.action.label(term)}</button>}
        </li>}
      </ul>
    </div>
    {hint && <p className="ui-hint" id={hintId}>{hint}</p>}
    {error && <p className="ui-error" id={errorId}>{error}</p>}
  </div>;
}
