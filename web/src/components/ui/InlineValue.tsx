import { useEffect, useRef, useState } from 'react';
import './inline-value.css';

type Props = {
  id: string;
  label: string;
  value: string;
  editing: boolean;
  onEditingChange: (editing: boolean) => void;
  onCommit: (value: string) => void;
  onTextChange: (value: string) => void;
  normalize: (value: string) => string | null;
  suffix?: string;
  error?: string;
  disabled?: boolean;
};

// Invalid committed text remains visible; the owner validates its whole draft.
export function InlineValue({ id, label, value, editing, onEditingChange, onCommit, onTextChange, normalize, suffix, error, disabled }: Props) {
  const [text, setText] = useState(value);
  const input = useRef<HTMLInputElement>(null), button = useRef<HTMLButtonElement>(null);
  const finished = useRef(false), restoreFocus = useRef(false), original = useRef(value);
  useEffect(() => {
    if (editing) {
      setText(value); original.current = value; finished.current = false;
      input.current?.focus(); input.current?.select();
    } else if (restoreFocus.current) {
      restoreFocus.current = false; button.current?.focus();
    }
  }, [editing]); // Value is captured when an edit opens; typing does not reset it.
  function finish(cancel: boolean, restore: boolean) {
    if (finished.current) return;
    finished.current = true; restoreFocus.current = restore;
    onCommit(cancel ? original.current : normalize(text) ?? text);
    onEditingChange(false);
  }
  const describedBy = error ? `${id}-error` : undefined;
  return <div className="ui-inline-value">
    {editing ? <div className={`ui-inline-value__editor ${error ? 'is-invalid' : ''}`}>
      <input ref={input} id={id} type="text" inputMode={suffix ? 'decimal' : 'text'} aria-label={label} aria-invalid={error ? true : undefined} aria-describedby={describedBy}
        value={text} disabled={disabled} onChange={event => { setText(event.target.value); onTextChange(event.target.value); }} onBlur={() => finish(false, false)}
        onKeyDown={event => {
          if (event.key === 'Enter' || event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); finish(event.key === 'Escape', true); }
        }} />
      {suffix && <span aria-hidden="true">{suffix}</span>}
    </div> : <button ref={button} id={id} type="button" className={`ui-inline-value__button ${error ? 'is-invalid' : ''}`} aria-label={`Edit ${label.charAt(0).toLowerCase()}${label.slice(1)}`}
      aria-invalid={error ? true : undefined} aria-describedby={[`${id}-value`, describedBy].filter(Boolean).join(' ')} title={value || undefined} disabled={disabled} onClick={() => onEditingChange(true)}>
      <span id={`${id}-value`}>{value || 'Set value'}</span>{suffix && <span className="ui-inline-value__suffix" aria-hidden="true">{suffix}</span>}
      <svg viewBox="0 0 16 16" aria-hidden="true"><path d="m3 10 7-7 3 3-7 7H3v-3Zm6-6 3 3" /></svg>
    </button>}
    {error && <p className="ui-error" id={describedBy}>{error}</p>}
  </div>;
}
