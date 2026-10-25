import type { ButtonHTMLAttributes, ReactNode, Ref } from 'react';

export type ButtonVariant = 'primary' | 'outline' | 'ghost' | 'danger';
type Props = ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: ButtonVariant;
  pending?: boolean;
  children: ReactNode;
  ref?: Ref<HTMLButtonElement>;
};

export function Button({ variant = 'primary', pending = false, disabled, type = 'button', className = '', children, ...props }: Props) {
  return (
    <button {...props} type={type} disabled={disabled || pending} aria-busy={pending || undefined} className={`ui-button ui-button--${variant} ${className}`.trim()}>
      {pending && <span className="ui-button__mark" aria-hidden="true" />}{children}
    </button>
  );
}
