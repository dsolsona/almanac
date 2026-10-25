import { useEffect, useRef, type ReactNode, type RefObject } from 'react';
import { Button } from './Button.tsx';

type Props = {
  open: boolean;
  onClose: () => void;
  title: string;
  children: ReactNode;
  triggerRef: RefObject<HTMLElement | null>;
};

export function Dialog({ open, onClose, title, children, triggerRef }: Props) {
  const dialogRef = useRef<HTMLDialogElement>(null);
  const wasOpen = useRef(false);
  useEffect(() => {
    const dialog = dialogRef.current;
    if (!dialog) return;
    if (open && !dialog.open) dialog.showModal();
    if (!open && dialog.open) dialog.close();
    if (wasOpen.current && !open) triggerRef.current?.focus();
    wasOpen.current = open;
  }, [open, triggerRef]);

  function requestClose() {
    onClose();
  }

  return <dialog ref={dialogRef} className="ui-dialog" aria-labelledby="ui-dialog-title" onCancel={(event) => { event.preventDefault(); requestClose(); }}>
    <header className="ui-dialog__header">
      <h2 id="ui-dialog-title">{title}</h2>
      <Button variant="ghost" aria-label="Close dialog" onClick={requestClose}>×</Button>
    </header>
    <div className="ui-dialog__content">{children}</div>
  </dialog>;
}
