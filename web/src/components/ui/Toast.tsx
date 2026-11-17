import { useEffect, useState } from 'react';
import { Button } from './Button.tsx';
import './toast.css';

type Props = { message: string; onDismiss: () => void; duration?: number };

// Keep the live region mounted across message changes. Success feedback never
// takes focus; reading with a pointer or keyboard pauses its dismissal.
export function Toast({ message, onDismiss, duration = 5000 }: Props) {
  const [hovered, setHovered] = useState(false), [focused, setFocused] = useState(false);
  useEffect(() => { if (!message) { setHovered(false); setFocused(false); } }, [message]);
  useEffect(() => {
    if (!message || hovered || focused) return;
    const timer = setTimeout(onDismiss, duration);
    return () => clearTimeout(timer);
  }, [message, onDismiss, duration, hovered, focused]);
  return <div className="ui-toast" data-open={!!message}
    onMouseEnter={() => setHovered(true)} onMouseLeave={() => setHovered(false)}
    onFocus={() => setFocused(true)} onBlur={event => { if (!event.currentTarget.contains(event.relatedTarget)) setFocused(false); }}>
    <span role="status" aria-atomic="true">{message}</span>
    {message && <Button variant="ghost" aria-label="Dismiss notification" title="Dismiss notification" onClick={onDismiss}>Dismiss</Button>}
  </div>;
}
