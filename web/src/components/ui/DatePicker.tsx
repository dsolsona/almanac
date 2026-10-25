import { useEffect, useId, useRef, useState, type KeyboardEvent } from 'react';
import { addDays, addMonths, daysInMonth, formatCalendarDate, parseCalendarDate, weekday, type CalendarDate } from './dateMath.ts';
import { Button } from './Button.tsx';
import { Dialog } from './Dialog.tsx';

type Props = {
  label: string;
  value: string;
  onValueChange: (value: string) => void;
  hint?: string;
  disabled?: boolean;
  today?: string;
};

const weekdays = ['Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday', 'Sunday'];
const formatter = new Intl.DateTimeFormat('en', { month: 'long', year: 'numeric', timeZone: 'UTC' });
const displayFormatter = new Intl.DateTimeFormat('en', { month: 'short', day: 'numeric', year: 'numeric', timeZone: 'UTC' });
const accessibleDateFormatter = new Intl.DateTimeFormat('en', { weekday: 'long', month: 'long', day: 'numeric', year: 'numeric', timeZone: 'UTC' });

function utcDate(year: number, month: number, day: number): Date {
  const value = new Date(0);
  value.setUTCHours(0, 0, 0, 0);
  value.setUTCFullYear(year, month, day);
  return value;
}

function monthLabel(date: CalendarDate): string {
  return formatter.format(utcDate(date.year, date.month - 1, 1));
}

function dateLabel(date: CalendarDate): string {
  return displayFormatter.format(utcDate(date.year, date.month - 1, date.day));
}

function accessibleDateLabel(date: CalendarDate): string {
  return accessibleDateFormatter.format(utcDate(date.year, date.month - 1, date.day));
}

export function DatePicker({ label, value, onValueChange, hint, disabled = false, today: todayProp }: Props) {
  const id = useId();
  const labelId = `${id}-label`;
  const valueId = `${id}-value`;
  const hintId = hint ? `${id}-hint` : undefined;
  const today = parseCalendarDate(todayProp ?? formatCalendarDate({ year: new Date().getFullYear(), month: new Date().getMonth() + 1, day: new Date().getDate() })) ?? { year: 2000, month: 1, day: 1 };
  const selected = parseCalendarDate(value);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const focusedRef = useRef<HTMLButtonElement>(null);
  const [open, setOpen] = useState(false);
  const [month, setMonth] = useState<CalendarDate>(selected ?? today);
  const [focused, setFocused] = useState<CalendarDate>(selected ?? today);

  useEffect(() => { if (open) focusedRef.current?.focus(); }, [open, focused, month]);

  function showCalendar() {
    setMonth(selected ?? today);
    setFocused(selected ?? today);
    setOpen(true);
  }

  function shiftMonth(amount: number) {
    const next = addMonths(month, amount);
    if (next.year < 1 || next.year > 9999) return;
    const nextFocused = addMonths(focused, amount);
    setMonth(next);
    setFocused(nextFocused);
  }

  function moveFocus(date: CalendarDate) {
    if (date.year < 1 || date.year > 9999) return;
    setFocused(date);
    setMonth({ ...date, day: 1 });
  }

  function handleKeyDown(event: KeyboardEvent<HTMLButtonElement>, date: CalendarDate) {
    let target: CalendarDate | null = null;
    if (event.key === 'ArrowLeft') target = addDays(date, -1);
    if (event.key === 'ArrowRight') target = addDays(date, 1);
    if (event.key === 'ArrowUp') target = addDays(date, -7);
    if (event.key === 'ArrowDown') target = addDays(date, 7);
    if (event.key === 'Home') target = addDays(date, -((weekday(date) + 6) % 7));
    if (event.key === 'End') target = addDays(date, 6 - ((weekday(date) + 6) % 7));
    if (event.key === 'PageUp') target = addMonths(date, event.shiftKey ? -12 : -1);
    if (event.key === 'PageDown') target = addMonths(date, event.shiftKey ? 12 : 1);
    if (target) { event.preventDefault(); moveFocus(target); }
  }

  const firstWeekday = (weekday({ ...month, day: 1 }) + 6) % 7;
  const days = daysInMonth(month.year, month.month);
  const rows = Math.ceil((firstWeekday + days) / 7);
  const cells = Array.from({ length: rows * 7 }, (_, index) => {
    const day = index - firstWeekday + 1;
    return day >= 1 && day <= days ? day : null;
  });

  return <div className="ui-date-picker">
    <span className="ui-date-picker__label" id={labelId}>{label}</span>
    <Button ref={triggerRef} variant="outline" disabled={disabled} aria-labelledby={`${labelId} ${valueId}`} aria-describedby={hintId} aria-haspopup="dialog" aria-expanded={open} onClick={showCalendar}>
      <span aria-hidden="true">▦</span> <span id={valueId}>{selected ? dateLabel(selected) : 'Choose date'}</span>
    </Button>
    {hint && <span className="ui-date-picker__hint" id={hintId}>{hint}</span>}
    <Dialog open={open} onClose={() => setOpen(false)} title={`Choose ${label}`} triggerRef={triggerRef} initialFocusRef={focusedRef}>
      <div className="ui-calendar">
        <div className="ui-calendar__navigation">
          <Button variant="ghost" aria-label="Previous month" disabled={month.year === 1 && month.month === 1} onClick={() => shiftMonth(-1)}>‹</Button>
          <h3 aria-live="polite" aria-atomic="true">{monthLabel(month)}</h3>
          <Button variant="ghost" aria-label="Next month" disabled={month.year === 9999 && month.month === 12} onClick={() => shiftMonth(1)}>›</Button>
        </div>
        <div className="ui-calendar__grid" role="grid" aria-label={monthLabel(month)}>
          <div role="row">{weekdays.map((day) => <span role="columnheader" className="ui-calendar__weekday" key={day} aria-label={day}>{day.slice(0, 2)}</span>)}</div>
          {Array.from({ length: rows }, (_, row) => <div role="row" key={`week-${row}`}>
            {cells.slice(row * 7, row * 7 + 7).map((day, column) => {
              if (!day) return <span role="gridcell" aria-hidden="true" className="ui-calendar__blank" key={`blank-${row}-${column}`} />;
              const date = { year: month.year, month: month.month, day };
              const dateValue = formatCalendarDate(date);
              const isSelected = selected !== null && dateValue === formatCalendarDate(selected);
              const isToday = dateValue === formatCalendarDate(today);
              const isFocused = dateValue === formatCalendarDate(focused);
              return <span role="gridcell" key={day} aria-selected={isSelected}>
                <button ref={isFocused ? focusedRef : undefined} type="button" tabIndex={isFocused ? 0 : -1} className={`ui-calendar__day${isSelected ? ' is-selected' : ''}${isToday ? ' is-today' : ''}`} aria-label={`${accessibleDateLabel(date)}${isSelected ? ', selected' : ''}${isToday ? ', today' : ''}`} aria-current={isToday ? 'date' : undefined} onKeyDown={(event) => handleKeyDown(event, date)} onClick={() => { onValueChange(dateValue); setOpen(false); }}>{day}</button>
              </span>;
            })}
          </div>)}
        </div>
        <div className="ui-calendar__actions">
          <Button variant="ghost" onClick={() => { onValueChange(''); setOpen(false); }}>Clear</Button>
          <Button variant="outline" onClick={() => setOpen(false)}>Cancel</Button>
        </div>
      </div>
    </Dialog>
  </div>;
}
