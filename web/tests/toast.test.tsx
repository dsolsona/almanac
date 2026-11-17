import { useState } from 'react';
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import { Toast } from '../src/components/ui/Toast.tsx';
afterEach(()=>{cleanup();vi.useRealTimers();});
it('announces success without moving focus and allows dismissal',()=>{
 const dismiss=vi.fn();render(<><button>Existing focus</button><Toast message="Saved." onDismiss={dismiss}/></>);
 const existing=screen.getByRole('button',{name:'Existing focus'});existing.focus();
 expect(screen.getByRole('status')).toHaveTextContent('Saved.');expect(existing).toHaveFocus();
 fireEvent.click(screen.getByRole('button',{name:'Dismiss notification'}));expect(dismiss).toHaveBeenCalledOnce();
});
it('pauses dismissal while reading with pointer or keyboard and resumes afterward',()=>{
 vi.useFakeTimers();const dismiss=vi.fn();render(<Toast message="Saved." onDismiss={dismiss}/>);
 const toast=screen.getByRole('status').parentElement!;
 fireEvent.mouseEnter(toast);act(()=>{vi.advanceTimersByTime(6000);});expect(dismiss).not.toHaveBeenCalled();
 fireEvent.focus(screen.getByRole('button',{name:'Dismiss notification'}));fireEvent.mouseLeave(toast);
 act(()=>{vi.advanceTimersByTime(6000);});expect(dismiss).not.toHaveBeenCalled();
 fireEvent.blur(screen.getByRole('button',{name:'Dismiss notification'}),{relatedTarget:document.body});
 act(()=>{vi.advanceTimersByTime(4999);});expect(dismiss).not.toHaveBeenCalled();
 act(()=>{vi.advanceTimersByTime(1);});expect(dismiss).toHaveBeenCalledOnce();
});
it('cleans up its timer when a message clears or the notification unmounts',()=>{
 vi.useFakeTimers();const dismiss=vi.fn();const view=render(<Toast message="Saved." onDismiss={dismiss}/>);
 view.rerender(<Toast message="" onDismiss={dismiss}/>);act(()=>{vi.advanceTimersByTime(6000);});expect(dismiss).not.toHaveBeenCalled();
 expect(screen.queryByRole('button',{name:'Dismiss notification'})).not.toBeInTheDocument();
 view.rerender(<Toast message="Saved again." onDismiss={dismiss}/>);view.unmount();
 act(()=>{vi.advanceTimersByTime(6000);});expect(dismiss).not.toHaveBeenCalled();
});

it('starts a fresh dismissal timer after a focused notification was dismissed',()=>{
 vi.useFakeTimers();
 function Example(){
  const [message,setMessage]=useState('First save.');
  return <><button onClick={()=>setMessage('Second save.')}>Save again</button><Toast message={message} onDismiss={()=>setMessage('')}/></>;
 }
 render(<Example/>);
 const dismiss=screen.getByRole('button',{name:'Dismiss notification'});
 fireEvent.mouseEnter(dismiss.parentElement!);act(()=>{dismiss.focus();});fireEvent.click(dismiss);
 expect(screen.getByRole('status')).toBeEmptyDOMElement();
 fireEvent.click(screen.getByRole('button',{name:'Save again'}));
 expect(screen.getByRole('status')).toHaveTextContent('Second save.');
 act(()=>{vi.advanceTimersByTime(5000);});
 expect(screen.getByRole('status')).toBeEmptyDOMElement();
});
