import { createRoot } from 'react-dom/client';
import { AuthPage } from './auth/AuthPage.tsx';
import './styles/global.css';

function NotFound() {
  return <>
    <a className="skip-link" href="#main">Skip to content</a>
    <header className="masthead"><p className="wordmark">ALMANAC</p></header>
    <main id="main" className="not-found"><p className="mono">404</p><h1>Page not found</h1><p>This address does not match a page in Almanac.</p></main>
  </>;
}

const root = createRoot(document.getElementById('root')!);
if (import.meta.env.DEV && window.location.pathname === '/design-system') {
  void import('./catalogue/Catalogue.tsx').then(({ Catalogue }) => root.render(<Catalogue />));
} else {
  root.render(window.location.pathname === '/' || window.location.pathname === '/agents' ? <AuthPage /> : <NotFound />);
}
