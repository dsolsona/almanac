import { useId, useRef, useState } from 'react';
import { Button, Calendar, Dialog, TextField } from '../components/ui/index.ts';
import { parseCalendarDate } from '../components/ui/dateMath.ts';

const dateLabel = new Intl.DateTimeFormat('en', { weekday: 'short', month: 'short', day: 'numeric', year: 'numeric', timeZone: 'UTC' });
const shortDate = (day: string) => dateLabel.format(new Date(`${day}T00:00:00Z`));
const clockLabel = (clock: string) => clock.replace(/^(\d\d:\d\d):00$/, '$1');

type WindowBounds = { start: string; end: string };

export function MaintenanceWindowPicker({ bounds: { start, end }, onChange, seconds = false, disabled = false }: {
  bounds: WindowBounds;
  onChange: (bounds: WindowBounds) => void;
  seconds?: boolean;
  disabled?: boolean;
}) {
  const id = useId();
  const trigger = useRef<HTMLButtonElement>(null);
  const focusedDay = useRef<HTMLButtonElement>(null);
  const [open, setOpen] = useState(false);
  const [startDay, setStartDay] = useState('');
  const [endDay, setEndDay] = useState('');
  const [startClock, setStartClock] = useState('');
  const [endClock, setEndClock] = useState('');
  const [choosingEnd, setChoosingEnd] = useState(false);
  const [calendarKey, setCalendarKey] = useState(0);

  const today = new Date();
  const tomorrow = new Date(today);
  tomorrow.setUTCDate(tomorrow.getUTCDate() + 1);
  const clockPattern = seconds ? /^([01]\d|2[0-3]):[0-5]\d(:[0-5]\d)?$/ : /^([01]\d|2[0-3]):[0-5]\d$/;
  const startUTC = `${startDay}T${startClock}`;
  const endUTC = `${endDay}T${endClock}`;
  const clocksValid = clockPattern.test(startClock) && clockPattern.test(endClock);
  const ordered = Date.parse(`${endUTC}Z`) > Date.parse(`${startUTC}Z`);
  const valid = clocksValid && ordered && startDay >= '1970-01-01';

  function begin() {
    const nextHour = new Date();
    nextHour.setUTCHours(nextHour.getUTCHours() + 1, 0, 0, 0);
    const previewStart = start || nextHour.toISOString().slice(0, 16);
    const previewEnd = end || new Date(nextHour.getTime() + 3_600_000).toISOString().slice(0, 16);
    setStartDay(previewStart.slice(0, 10));
    setStartClock(clockLabel(previewStart.slice(11)));
    setEndDay(previewEnd.slice(0, 10));
    setEndClock(clockLabel(previewEnd.slice(11)));
    setChoosingEnd(false);
    setOpen(true);
  }

  function chooseDay(day: string) {
    if (choosingEnd) {
      setStartDay(day < startDay ? day : startDay);
      setEndDay(day < startDay ? startDay : day);
    } else {
      setStartDay(day);
      setEndDay(day);
    }
    setChoosingEnd(!choosingEnd);
  }

  function jumpTo(date: Date) {
    const day = date.toISOString().slice(0, 10);
    setStartDay(day);
    setEndDay(day);
    setChoosingEnd(true);
    setCalendarKey(key => key + 1);
  }

  function apply() {
    if (!valid) return;
    onChange({ start: startUTC, end: endUTC });
    setOpen(false);
  }

  const selectedStart = start && `${shortDate(start.slice(0, 10))} · ${clockLabel(start.slice(11))}`;
  const selectedEnd = end && (start.slice(0, 10) === end.slice(0, 10)
    ? clockLabel(end.slice(11))
    : `${shortDate(end.slice(0, 10))} · ${clockLabel(end.slice(11))}`);

  return <div className="ui-field maintenance-start-field">
    <span id={id} className="ui-label">Maintenance window</span>
    <Button ref={trigger} variant="outline" disabled={disabled} className="maintenance-start-trigger"
      aria-labelledby={`${id} ${id}-value`} aria-haspopup="dialog" aria-expanded={open} onClick={begin}>
      <span aria-hidden="true">▦</span>
      <span id={`${id}-value`}>{selectedStart ? `${selectedStart}${selectedEnd ? ` → ${selectedEnd}` : ''} UTC` : 'Choose dates and times'}</span>
    </Button>
    <Dialog open={open} onClose={() => setOpen(false)} title="Choose maintenance window" triggerRef={trigger} initialFocusRef={focusedDay}>
      {open && <div className="maintenance-start-dialog">
        <div className="maintenance-date-shortcuts">
          <Button variant="outline" onClick={() => jumpTo(today)}>Today</Button>
          <Button variant="outline" onClick={() => jumpTo(tomorrow)}>Tomorrow</Button>
          <span aria-live="polite">{choosingEnd ? 'Choose end date' : 'Choose start date'}</span>
        </div>
        <div className="maintenance-start-calendar">
          <Calendar key={calendarKey} value={startDay} rangeEnd={endDay} today={parseCalendarDate(today.toISOString().slice(0, 10))!}
            focusedRef={focusedDay} onValueChange={chooseDay} />
        </div>
        <div className="maintenance-window-clocks">
          <ClockField label="Start time (UTC)" day={startDay} value={startClock} seconds={seconds} onChange={setStartClock} onEnter={apply} />
          <ClockField label="End time (UTC)" day={endDay} value={endClock} seconds={seconds} onChange={setEndClock} onEnter={apply} />
        </div>
        {clocksValid && !ordered && <p className="ui-error" role="status">End must be after start.</p>}
        {startDay < '1970-01-01' && <p className="ui-error" role="status">Choose a start on or after January 1, 1970.</p>}
        <div className="maintenance-start-actions">
          <Button variant="outline" onClick={() => setOpen(false)}>Cancel</Button>
          <Button disabled={!valid} onClick={apply}>Set window</Button>
        </div>
      </div>}
    </Dialog>
  </div>;
}

function ClockField({ label, day, value, seconds, onChange, onEnter }: {
  label: string; day: string; value: string; seconds: boolean; onChange: (value: string) => void; onEnter: () => void;
}) {
  const id = useId();

  return <TextField id={id} label={label} value={value} onChange={event => onChange(event.target.value)}
    hint={shortDate(day)} placeholder={seconds ? 'HH:MM:SS' : 'HH:MM'} autoComplete="off" spellCheck={false} maxLength={seconds ? 8 : 5}
    onFocus={event => event.currentTarget.select()}
    onMouseDown={event => {
      if (event.button !== 0 || event.currentTarget === document.activeElement) return;
      // Keep the focus selection from collapsing to the first click's caret position.
      event.preventDefault();
      event.currentTarget.focus();
    }}
    onKeyDown={event => { if (event.key === 'Enter') { event.preventDefault(); onEnter(); } }} />;
}
