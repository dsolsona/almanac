import type { ReactNode } from 'react';

export function Panel({ heading, children, className = '' }: { heading: string; children: ReactNode; className?: string }) {
  return <section className={`ui-panel ${className}`.trim()} aria-labelledby={`panel-${heading.toLowerCase().replace(/[^a-z0-9]+/g, '-')}`}>
    <h2 id={`panel-${heading.toLowerCase().replace(/[^a-z0-9]+/g, '-')}`}>{heading}</h2>
    {children}
  </section>;
}
