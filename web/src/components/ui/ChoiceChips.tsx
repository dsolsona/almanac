import { useEffect, useRef, useState, type InputHTMLAttributes } from 'react';
import { moveRadioFocus } from './radioGroup.ts';
import './operational-controls.css';

export type ChipOption = { value: string; label: string };
export type CustomChip = {
  unit?: string;
  placeholder?: string;
  format: (value: string) => string;
  validate?: (value: string) => string | undefined;
  inputMode?: InputHTMLAttributes<HTMLInputElement>['inputMode'];
};

type Props = {
  id: string;
  label: string;
  options: ChipOption[];
  value: string;
  onChange: (value: string) => void;
  custom?: CustomChip;
  hint?: string;
  error?: string;
  disabled?: boolean;
};

// Presets as chips. The dashed "Custom…" chip reveals an input that applies as you type,
// relabels the chip with the formatted value and collapses on Enter or blur.
export function ChoiceChips({ id, label, options, value, onChange, custom, hint, error, disabled }: Props) {
  const isPreset = options.some((option) => option.value === value);
  const [customOpen, setCustomOpen] = useState(false);
  const [customText, setCustomText] = useState(isPreset ? '' : value);
  const input = useRef<HTMLInputElement>(null);
  const customSelected = !isPreset && (customOpen || value !== '');
  const customError = custom?.validate && customSelected && customText !== '' ? custom.validate(customText) : undefined;
  const shownError = error ?? customError;
  const hintId = hint ? `${id}-hint` : undefined;
  const errorId = shownError ? `${id}-error` : undefined;
  const describedBy = [hintId, errorId].filter(Boolean).join(' ') || undefined;

  useEffect(() => { if (customOpen) input.current?.focus(); }, [customOpen]);
  // A parent-driven custom value (reset, replay) replaces the typed text so the chip never reapplies stale input.
  useEffect(() => { if (!isPreset && value !== customText.trim()) setCustomText(value); }, [value]); // eslint-disable-line react-hooks/exhaustive-deps

  function choosePreset(next: string) {
    setCustomOpen(false);
    onChange(next);
  }
  function openCustom() {
    setCustomOpen(true);
    if (customText !== '' && customText !== value) onChange(customText);
  }
  // Applies as typed, including a cleared input; validation only reports. The owning form rejects an invalid or empty value on submit.
  function applyCustom(text: string) {
    setCustomText(text);
    onChange(text.trim());
  }
  function collapse() {
    if (customText.trim() !== '') setCustomOpen(false);
  }

  return <div className="ui-field ui-chips-field">
    <span className="ui-label" id={`${id}-label`}>{label}</span>
    <div role="radiogroup" aria-labelledby={`${id}-label`} aria-describedby={describedBy} className="ui-chips" onKeyDown={(event) => moveRadioFocus(event, (next) => next === '__custom' ? openCustom() : choosePreset(next))}>
      {options.map((option) => <button
        key={option.value}
        type="button"
        role="radio"
        value={option.value}
        aria-checked={!customSelected && option.value === value}
        tabIndex={!customSelected && option.value === value ? 0 : -1}
        disabled={disabled}
        className="ui-chip"
        onClick={() => choosePreset(option.value)}
      >{option.label}</button>)}
      {custom && <button
        type="button"
        role="radio"
        value="__custom"
        aria-checked={customSelected}
        aria-controls={`${id}-custom`}
        aria-expanded={customOpen}
        tabIndex={customSelected || !isPreset ? 0 : -1}
        disabled={disabled}
        className={`ui-chip ui-chip--custom ${shownError && customSelected ? 'ui-chip--invalid' : ''}`.trim()}
        onClick={openCustom}
      >{customSelected && customText.trim() !== '' && !customError ? `Custom · ${custom.format(customText.trim())}` : 'Custom…'}</button>}
    </div>
    {custom && <div id={`${id}-custom`} className="ui-chips__custom" hidden={!customOpen}>
      <input
        ref={input}
        id={id}
        aria-label={`Custom ${label.toLowerCase()}`}
        aria-invalid={customError ? true : undefined}
        aria-describedby={describedBy}
        className={`ui-control ${customError ? 'ui-control--invalid' : ''}`.trim()}
        inputMode={custom.inputMode ?? 'decimal'}
        placeholder={custom.placeholder}
        value={customText}
        disabled={disabled}
        onChange={(event) => applyCustom(event.target.value)}
        onBlur={collapse}
        onKeyDown={(event) => { if (event.key === 'Enter') { event.preventDefault(); collapse(); input.current?.blur(); } if (event.key === 'Escape') { event.preventDefault(); setCustomOpen(false); } }}
      />
      {custom.unit && <span className="ui-chips__unit" aria-hidden="true">{custom.unit}</span>}
    </div>}
    {hint && <p className="ui-hint" id={hintId}>{hint}</p>}
    {shownError && <p className="ui-error" id={errorId} role="alert">{shownError}</p>}
  </div>;
}
