import { useId, type ReactNode } from 'react';
import './operational-controls.css';

type SectionProps = {
  title: string;
  step?: number | string;
  hint?: ReactNode;
  tools?: ReactNode;
  muted?: boolean;
  id?: string;
  className?: string;
  children: ReactNode;
};

// One numbered step of a form (ADR-0028): heading row with an optional control and a
// right-aligned mono hint, then a body that stacks fields and two-column grids.
export function FormSection({ title, step, hint, tools, muted, id, className = '', children }: SectionProps) {
  const generated = useId();
  const headingId = `${id ?? generated}-title`;
  return <section id={id} aria-labelledby={headingId} className={`ui-form-section ${muted ? 'ui-form-section--muted' : ''} ${className}`.trim()}>
    <header className="ui-form-section__head">
      {step !== undefined && <span className="ui-form-section__step">{step}</span>}
      <h2 id={headingId} className="ui-form-section__title">{title}</h2>
      {tools && <div className="ui-form-section__tools">{tools}</div>}
      {hint && <span className="ui-form-section__hint">{hint}</span>}
    </header>
    <div className="ui-form-section__body">{children}</div>
  </section>;
}

// The closing row of a form: a mono note on the left, the buttons on the right.
export function FormActions({ note, children, className = '' }: { note?: ReactNode; children: ReactNode; className?: string }) {
  return <div className={`ui-form-actions ${className}`.trim()}>
    {note && <span className="ui-form-actions__note">{note}</span>}
    <div className="ui-form-actions__buttons">{children}</div>
  </div>;
}
