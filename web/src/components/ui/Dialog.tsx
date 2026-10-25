import { useEffect, useId, useRef, type ReactNode, type RefObject } from 'react';
import { Button } from './Button.tsx';

type Props = {
  open: boolean;
  onClose: () => void;
  title: string;
  children: ReactNode;
  triggerRef: RefObject<HTMLElement | null>;
  initialFocusRef?: RefObject<HTMLElement | null>;
};

export function Dialog({ open, onClose, title, children, triggerRef, initialFocusRef }: Props) {
  const titleId = `${useId()}-title`;
  const dialogRef = useRef<HTMLDialogElement>(null);
  const wasOpen = useRef(false);
  useEffect(() => {
    const dialog = dialogRef.current;
    if (!dialog) return;
    if (open && !dialog.open) {
      dialog.showModal();
      initialFocusRef?.current?.focus();
    }
    if (!open && dialog.open) dialog.close();
    if (wasOpen.current && !open) triggerRef.current?.focus();
    wasOpen.current = open;
  }, [open, triggerRef, initialFocusRef]);

  function requestClose() {
    onClose();
  }

  return <dialog ref={dialogRef} className="ui-dialog" aria-labelledby={titleId} onCancel={(event) => { event.preventDefault(); requestClose(); }}>
    <header className="ui-dialog__header">
      <h2 id={titleId}>{title}</h2>
      <Button variant="ghost" aria-label="Close dialog" onClick={requestClose}>×</Button>
    </header>
    <div className="ui-dialog__content">{children}</div>
  </dialog>;
}
