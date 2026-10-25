import { createRoot } from 'react-dom/client';
import './styles/global.css';

function Shell({ notFound = false }: { notFound?: boolean }) {
  return <>
    <a className="skip-link" href="#main">Skip to content</a>
    <header className="masthead"><p className="wordmark">ALMANAC</p></header>
    <main id="main" className={notFound ? 'not-found' : 'app-shell'}>
      {notFound ? <><p className="mono">404</p><h1>Page not found</h1><p>This address does not match a page in Almanac.</p></> : <><p className="mono">APPLICATION FOUNDATION</p><h1>Almanac is being built.</h1><p>This local shell establishes the visual foundation. Product screens will appear here as they are delivered.</p></>}
    </main>
  </>;
}

const root = createRoot(document.getElementById('root')!);
if (import.meta.env.DEV && window.location.pathname === '/design-system') {
  void import('./catalogue/Catalogue.tsx').then(({ Catalogue }) => root.render(<Catalogue />));
} else {
  root.render(<Shell notFound={window.location.pathname !== '/'} />);
}
