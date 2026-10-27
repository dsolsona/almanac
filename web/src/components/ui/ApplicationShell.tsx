import { useEffect, useLayoutEffect, useRef, useState, type KeyboardEvent, type MouseEvent, type ReactNode } from 'react';
import { Button } from './Button.tsx';
import { ThemeToggle } from './ThemeToggle.tsx';
import './application-shell.css';

const destinations = ['SLOs', 'SLIs', 'Datasources', 'Alerts'];
const selectionParams = ['agent', 'agents_cursor', 'credentials_cursor'];
const mobileViewport = () => typeof window.matchMedia === 'function' && window.matchMedia('(max-width: 860px)').matches;

export function ApplicationShell({ login, role, pending, showCheckSession, onCheckSession, onSignOut, embedded = false, children }: {
  login: string;
  role: string;
  pending: boolean;
  showCheckSession: boolean;
  onCheckSession: () => void;
  onSignOut: () => void;
  embedded?: boolean;
  children: ReactNode;
}) {
  const [open, setOpen] = useState(false);
  const menuTrigger = useRef<HTMLButtonElement>(null);
  const closeTrigger = useRef<HTMLButtonElement>(null);
  const currentDestination = useRef<HTMLAnchorElement>(null);
  const wasOpen = useRef(false);

  useLayoutEffect(() => {
    if (open) {
      wasOpen.current = true;
      closeTrigger.current?.focus();
    } else if (wasOpen.current) {
      wasOpen.current = false;
      if (mobileViewport()) menuTrigger.current?.focus();
      else currentDestination.current?.focus();
    }
  }, [open]);

  useEffect(() => {
    if (!open) return;
    const closeForDesktop = () => {
      if (!mobileViewport()) setOpen(false);
    };
    window.addEventListener('resize', closeForDesktop);
    return () => window.removeEventListener('resize', closeForDesktop);
  }, [open]);

  function navigateToAgents(event: MouseEvent<HTMLAnchorElement>) {
    if (embedded) {
      event.preventDefault();
      return;
    }
    if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    event.preventDefault();
    const query = new URLSearchParams(location.search);
    for (const parameter of selectionParams) query.delete(parameter);
    const next = `/agents${query.size ? `?${query}` : ''}${location.hash}`;
    if (`${location.pathname}${location.search}${location.hash}` !== next) {
      history.pushState({}, '', next);
      window.dispatchEvent(new PopStateEvent('popstate'));
    }
    setOpen(false);
  }

  function containDrawerFocus(event: KeyboardEvent<HTMLElement>) {
    if (!open) return;
    if (event.key === 'Escape') {
      event.preventDefault();
      setOpen(false);
      return;
    }
    if (event.key !== 'Tab') return;
    const controls = Array.from(event.currentTarget.querySelectorAll<HTMLElement>('button:not(:disabled), a[href], [tabindex="0"]'));
    if (!controls.length) return;
    const first = controls[0]!;
    const last = controls[controls.length - 1]!;
    if (event.shiftKey && (document.activeElement === first || !event.currentTarget.contains(document.activeElement))) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && (document.activeElement === last || !event.currentTarget.contains(document.activeElement))) {
      event.preventDefault();
      first.focus();
    }
  }

  return <div className={`application-shell${embedded ? ' application-shell--embedded' : ''}`}>
    {!embedded && <a className="skip-link" href="#main">Skip to content</a>}
    <header className="app-mobile-header" inert={open || undefined}>
      <a className="wordmark" href="/agents" onClick={navigateToAgents}>ALMANAC</a>
      <button ref={menuTrigger} className="app-menu-trigger" type="button" aria-expanded={open} aria-controls="app-navigation" onClick={() => setOpen(true)}>
        <span aria-hidden="true">☰</span><span>Menu</span>
      </button>
    </header>
    <div className={`app-drawer-scrim${open ? ' is-open' : ''}`} aria-hidden="true" onClick={() => setOpen(false)} />
    <aside id="app-navigation" className={`app-sidebar${open ? ' is-open' : ''}`} role={open && !embedded ? 'dialog' : undefined} aria-modal={open && !embedded ? 'true' : undefined} aria-label={open && !embedded ? 'Navigation menu' : undefined} onKeyDown={containDrawerFocus}>
      <div className="app-sidebar__brand"><button ref={closeTrigger} className="app-drawer-close" type="button" aria-label="Close navigation" onClick={() => setOpen(false)}>×</button><a className="wordmark" href="/agents" onClick={navigateToAgents}>ALMANAC</a></div>
      <nav className="app-navigation" aria-label="Primary navigation">
        <a ref={currentDestination} className="app-navigation__current" href="/agents" aria-current="page" onClick={navigateToAgents}><span aria-hidden="true" className="app-navigation__mark">◈</span>Agents</a>
        {destinations.map((destination) => <div className="app-navigation__planned" key={destination} aria-disabled="true">
          <span><span aria-hidden="true" className="app-navigation__mark">·</span>{destination}</span>
          <small>Not available yet</small>
        </div>)}
      </nav>
      <footer className="app-account">
        <div className="app-account__identity"><span className="app-account__avatar" aria-hidden="true">{login.slice(0, 1).toUpperCase()}</span><span className="app-account__copy"><strong>{login}</strong><small>{role}</small></span></div>
        <ThemeToggle />
        {showCheckSession && <Button variant="outline" onClick={() => { setOpen(false); onCheckSession(); }} pending={pending}>Check session</Button>}
        <Button variant="outline" onClick={() => { setOpen(false); onSignOut(); }} pending={pending}>Sign out</Button>
      </footer>
    </aside>
    <div className="app-workspace" inert={open || undefined}>
      {embedded ? <div id="app-shell-preview-main" className="app-main">{children}</div> : <main id="main" className="app-main">{children}</main>}
    </div>
  </div>;
}
