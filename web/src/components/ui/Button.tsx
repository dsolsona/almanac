import type { ButtonHTMLAttributes, ReactNode, Ref } from 'react';

// Roles (ADR-0031, design guide "Button roles"): primary is the one main action of a view;
// outline is secondary; ghost is quiet (utility, icon); link is inline row and in-text actions;
// danger-outline starts a destructive action; danger (filled) only confirms one in its dialog;
// selected is the pressed state of a toggle, never an action.
export type ButtonVariant = 'primary' | 'selected' | 'outline' | 'ghost' | 'link' | 'danger-outline' | 'danger';
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
