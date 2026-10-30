import { useMemo, useRef, type TextareaHTMLAttributes } from 'react';
import './measurement-controls.css';

type Props = Omit<TextareaHTMLAttributes<HTMLTextAreaElement>, 'id' | 'value' | 'onChange'> & {
  id: string;
  label: string;
  value: string;
  onChange: (value: string) => void;
};

export function QueryEditor({ id, label, value, onChange, className = '', onScroll, ...props }: Props) {
  const lineNumbers = useRef<HTMLDivElement>(null);
  const lines = useMemo(() => Array.from({ length: Math.max(1, value.split('\n').length) }, (_, index) => index + 1), [value]);

  return <div className="query-editor">
    <div className="query-editor__label">
      <label className="ui-label" htmlFor={id}>{label}</label>
      <span className="query-editor__language" id={`${id}-language`}>PromQL</span>
    </div>
    <div className="query-editor__surface">
      <div className="query-editor__line-numbers" ref={lineNumbers} aria-hidden="true">
        {lines.map((line) => <span key={line}>{line}</span>)}
      </div>
      <textarea
        {...props}
        id={id}
        aria-describedby={[props['aria-describedby'], `${id}-language`].filter(Boolean).join(' ')}
        value={value}
        onChange={(event) => onChange(event.currentTarget.value)}
        onScroll={(event) => {
          if (lineNumbers.current) lineNumbers.current.scrollTop = event.currentTarget.scrollTop;
          onScroll?.(event);
        }}
        className={`ui-control query-editor__input ${className}`.trim()}
        spellCheck={false}
      />
    </div>
  </div>;
}
