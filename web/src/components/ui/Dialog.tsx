import { useEffect, useId, useRef, type KeyboardEvent, type ReactNode, type RefObject } from 'react';
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

  function containFocus(event:KeyboardEvent<HTMLDialogElement>) {
    if(event.key!=='Tab')return;
    const controls=[...event.currentTarget.querySelectorAll<HTMLElement>('a[href],button,input,select,textarea,[tabindex]')].filter(node=>node.tabIndex>=0&&!node.matches(':disabled')&&node.getClientRects().length>0&&getComputedStyle(node).visibility!=='hidden');
    const first=controls[0],last=controls.at(-1);
    if(event.shiftKey&&document.activeElement===first){event.preventDefault();last?.focus();}
    else if(!event.shiftKey&&document.activeElement===last){event.preventDefault();first?.focus();}
  }

  return <dialog ref={dialogRef} className="ui-dialog" aria-labelledby={titleId} onKeyDown={containFocus} onCancel={(event) => { event.preventDefault(); requestClose(); }}>
    <header className="ui-dialog__header">
      <h2 id={titleId}>{title}</h2>
      <Button variant="ghost" aria-label="Close dialog" onClick={requestClose}>×</Button>
    </header>
    <div className="ui-dialog__content">{children}</div>
  </dialog>;
}
