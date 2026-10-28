import { useEffect, useLayoutEffect, useRef, useState, type KeyboardEvent, type MouseEvent, type ReactNode } from 'react';
import { Button } from './Button.tsx';
import { ThemeToggle } from './ThemeToggle.tsx';
import './application-shell.css';

const mobileViewport = () => typeof window.matchMedia === 'function' && window.matchMedia('(max-width: 860px)').matches;
type IconName = 'slos' | 'slis' | 'alerts' | 'datasources' | 'agents';

function NavIcon({ name }: { name: IconName }) {
  const paths: Record<IconName, ReactNode> = {
    slos: <><circle cx="12" cy="12" r="8.5"/><path d="M12 7v5l3.5 2"/></>,
    slis: <><path d="M3 12h3l2-6 4 12 2-6h7"/></>,
    alerts: <><path d="M18 9a6 6 0 0 0-12 0c0 7-3 7-3 9h18c0-2-3-2-3-9"/><path d="M10 21h4"/></>,
    datasources: <><ellipse cx="12" cy="5" rx="8.5" ry="3"/><path d="M3.5 5v7c0 1.7 3.8 3 8.5 3s8.5-1.3 8.5-3V5M3.5 12v7c0 1.7 3.8 3 8.5 3s8.5-1.3 8.5-3v-7"/></>,
    agents: <><circle cx="8" cy="8" r="3"/><circle cx="17" cy="9" r="2.5"/><path d="M2.5 20c.4-3.4 2.3-5 5.5-5s5.1 1.6 5.5 5M14 15c3.8-.8 6.8.7 7.5 4"/></>,
  };
  return <svg className="app-navigation__icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">{paths[name]}</svg>;
}

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
  const [pathname, setPathname] = useState(() => window.location.pathname);
  const menuTrigger = useRef<HTMLButtonElement>(null);
  const closeTrigger = useRef<HTMLButtonElement>(null);
  const currentDestination = useRef<HTMLAnchorElement>(null);
  const wasOpen = useRef(false);
  const datasourceCurrent = pathname.startsWith('/datasources');
  const sliCurrent = pathname.startsWith('/slis');
  const agentCurrent = embedded || pathname === '/' || pathname.startsWith('/agents');

  useEffect(() => {
    const changed = () => setPathname(window.location.pathname);
    window.addEventListener('popstate', changed);
    return () => window.removeEventListener('popstate', changed);
  }, []);

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

  function navigate(event: MouseEvent<HTMLElement>) {
    const anchor = event.currentTarget instanceof HTMLAnchorElement
      ? event.currentTarget
      : event.target instanceof Element ? event.target.closest<HTMLAnchorElement>('a[data-app-route]') : null;
    if (!anchor) return;
    if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    if ((anchor.target && anchor.target !== '_self') || anchor.hasAttribute('download')) return;
    if (embedded) {
      event.preventDefault();
      return;
    }
    const next = new URL(anchor.href, window.location.href);
    if (next.origin !== window.location.origin || !(/^\/(?:agents|datasources(?:\/new|\/[0-9a-f-]{36})?|slis(?:\/new|\/[0-9a-f-]{36})?)?$/.test(next.pathname))) return;
    event.preventDefault();
    const query = new URLSearchParams(next.search);
    const path = `${next.pathname === '/' ? '/agents' : next.pathname}${query.size ? `?${query}` : ''}${next.hash}`;
    if (`${window.location.pathname}${window.location.search}${window.location.hash}` !== path) {
      window.history.pushState({}, '', path);
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
      <a className="wordmark" href="/agents" onClick={navigate}>ALMANAC</a>
      <button ref={menuTrigger} className="app-menu-trigger" type="button" aria-expanded={open} aria-controls="app-navigation" onClick={() => setOpen(true)}>
        <span aria-hidden="true">☰</span><span>Menu</span>
      </button>
    </header>
    <div className={`app-drawer-scrim${open ? ' is-open' : ''}`} aria-hidden="true" onClick={() => setOpen(false)} />
    <aside id="app-navigation" className={`app-sidebar${open ? ' is-open' : ''}`} role={open && !embedded ? 'dialog' : undefined} aria-modal={open && !embedded ? 'true' : undefined} aria-label={open && !embedded ? 'Navigation menu' : undefined} onKeyDown={containDrawerFocus}>
      <div className="app-sidebar__brand"><button ref={closeTrigger} className="app-drawer-close" type="button" aria-label="Close navigation" onClick={() => setOpen(false)}>×</button><a className="wordmark" href="/agents" onClick={navigate}>ALMANAC</a><span>Service reliability</span></div>
      <nav id="app-navigation-list" className="app-navigation" aria-label="Primary navigation">
        <div className="app-navigation__group"><h2>Product</h2>
          <div className="app-navigation__items">
            <div className="app-navigation__planned" aria-disabled="true"><span><NavIcon name="slos"/>SLOs</span><small>Not available yet</small></div>
            <a ref={sliCurrent ? currentDestination : undefined} className={sliCurrent ? 'app-navigation__current' : ''} href="/slis" aria-current={sliCurrent ? 'page' : undefined} onClick={navigate}><NavIcon name="slis"/>SLIs</a>
          </div>
        </div>
        <div className="app-navigation__group"><h2>Operations</h2>
          <div className="app-navigation__items"><div className="app-navigation__planned" aria-disabled="true"><span><NavIcon name="alerts"/>Alerts</span><small>Not available yet</small></div></div>
        </div>
        <div className="app-navigation__group"><h2>Administration</h2>
          <div className="app-navigation__items">
            <a ref={datasourceCurrent ? currentDestination : undefined} className={datasourceCurrent ? 'app-navigation__current' : ''} href="/datasources" aria-current={datasourceCurrent ? 'page' : undefined} onClick={navigate}><NavIcon name="datasources"/>Datasources</a>
            <a ref={agentCurrent ? currentDestination : undefined} className={agentCurrent ? 'app-navigation__current' : ''} href="/agents" aria-current={agentCurrent ? 'page' : undefined} onClick={navigate}><NavIcon name="agents"/>Agents</a>
          </div>
        </div>
      </nav>
      <footer className="app-account">
        <div className="app-account__identity"><span className="app-account__avatar" aria-hidden="true">{login.slice(0, 1).toUpperCase()}</span><span className="app-account__copy"><strong>{login}</strong><small>{role}</small></span></div>
        <ThemeToggle />
        {showCheckSession && <Button variant="outline" onClick={() => { setOpen(false); onCheckSession(); }} pending={pending}>Check session</Button>}
        <Button variant="outline" onClick={() => { setOpen(false); onSignOut(); }} pending={pending}>Sign out</Button>
      </footer>
    </aside>
    <div className="app-workspace" inert={open || undefined} onClick={!embedded ? navigate : undefined}>
      {embedded ? <div id="app-shell-preview-main" className="app-main">{children}</div> : <main id="main" className="app-main">{children}</main>}
    </div>
  </div>;
}
