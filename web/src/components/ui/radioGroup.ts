import type { KeyboardEvent } from 'react';

// Roving focus for groups of role="radio" buttons: arrows and Home/End move and select.
export function moveRadioFocus(event: KeyboardEvent<HTMLElement>, select: (value: string) => void): void {
  const keys = ['ArrowRight', 'ArrowDown', 'ArrowLeft', 'ArrowUp', 'Home', 'End'];
  if (!keys.includes(event.key)) return;
  const radios = Array.from(event.currentTarget.querySelectorAll<HTMLButtonElement>('[role="radio"]:not(:disabled)'));
  const current = radios.indexOf(event.target as HTMLButtonElement);
  if (radios.length === 0 || current < 0) return;
  event.preventDefault();
  const last = radios.length - 1;
  const next = event.key === 'Home' ? 0 : event.key === 'End' ? last
    : event.key === 'ArrowRight' || event.key === 'ArrowDown' ? (current === last ? 0 : current + 1)
      : (current === 0 ? last : current - 1);
  const target = radios[next]!;
  target.focus();
  select(target.value);
}
