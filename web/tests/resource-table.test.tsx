import {act, cleanup, fireEvent, render, renderHook, screen, waitFor} from '@testing-library/react';
import {afterEach, describe, expect, it, vi} from 'vitest';
import {ResourceTableHead, resourceRowClick, useResourceTable, type ResourceColumn} from '../src/components/ui/ResourceTable.tsx';

afterEach(() => {cleanup(); vi.restoreAllMocks();});
type Item = {id: string; name: string; count: number | null};
const items: Item[] = [{id:'a', name:'Zulu', count:2}];
const columns: ResourceColumn<Item>[] = [{id:'name',label:'Name',value:item=>item.name},{id:'count',label:'Count',value:item=>item.count}];

describe('resource tables', () => {
  it('sorts the complete result before paging, with numeric ordering and missing values last', async () => {
    const loadPage = vi.fn(async (cursor: string) => cursor ? {items:[{id:'b',name:'Alpha',count:10}, {id:'c',name:'Unknown',count:null}],next_cursor:''} : {items,next_cursor:'second'});
    const {result} = renderHook(() => useResourceTable({items, loadPage, pageSize:1}));
    expect(loadPage).not.toHaveBeenCalled();
    act(() => result.current.choose('name'));
    await waitFor(() => expect(result.current.busy).toBe(false));
    expect(loadPage.mock.calls.map(call=>call[0])).toEqual(['','second']);
    expect(result.current.rows(columns).items[0]?.name).toBe('Alpha');
    expect(result.current.rows(columns).total).toBe(3);
    act(() => result.current.next());
    expect(result.current.rows(columns).items[0]?.name).toBe('Unknown');
    act(() => result.current.choose('count'));
    expect(result.current.rows(columns).items[0]?.count).toBe(2);
    act(() => result.current.choose('count'));
    expect(result.current.rows(columns).items[0]?.count).toBe(10);
    act(() => result.current.next());
    expect(result.current.rows(columns).items[0]?.count).toBe(2);
    act(() => result.current.next());
    expect(result.current.rows(columns).items[0]?.count).toBeNull();
    expect(loadPage).toHaveBeenCalledTimes(2);
  });

  it('filters before pagination and breaks equal keys deterministically', async () => {
    const loadPage = async () => ({items:[{id:'z',name:'same',count:2},{id:'a',name:'same',count:2},{id:'x',name:'other',count:10}],next_cursor:''});
    const {result} = renderHook(() => useResourceTable({items,loadPage,pageSize:1}));
    act(() => result.current.choose('count'));
    await waitFor(() => expect(result.current.busy).toBe(false));
    expect(result.current.rows(columns, item=>item.name==='same')).toMatchObject({total:2,items:[{id:'a'}]});
    act(() => result.current.next());
    expect(result.current.rows(columns, item=>item.name==='same').items[0]?.id).toBe('z');
  });

  it('never exposes partial global results after a failed page or cyclic cursor', async () => {
    const loadPage = vi.fn(async (cursor: string) => {if(cursor) throw new Error('offline'); return {items,next_cursor:'second'};});
    const {result} = renderHook(() => useResourceTable({items,loadPage,pageSize:10}));
    act(() => result.current.choose('name'));
    await waitFor(() => expect(result.current.error).toBe('offline'));
    expect(result.current.rows(columns).total).toBe(0);
    loadPage.mockImplementation(async () => ({items,next_cursor:'second'}));
    act(() => result.current.refresh());
    await waitFor(() => expect(result.current.error).toContain('list changed'));
    expect(result.current.rows(columns).total).toBe(0);
  });

  it('cancels collection on unmount', async () => {
    let signal: AbortSignal | undefined;
    const loadPage = (_: string, requestSignal: AbortSignal) => {signal=requestSignal; return new Promise<{items:Item[];next_cursor:string}>(()=>{});};
    const {result,unmount} = renderHook(() => useResourceTable({items,loadPage,pageSize:10}));
    act(() => result.current.choose('name'));
    expect(signal?.aborted).toBe(false);
    unmount();
    expect(signal?.aborted).toBe(true);
  });

  it('announces header sort direction and preserves inner links, modifiers and text selection', () => {
    const choose = vi.fn(),open = vi.fn();
    render(<table><ResourceTableHead columns={columns} sorting={{sort:{field:'name',descending:true},choose}}/><tbody><tr onClick={event=>resourceRowClick(event,open)}><td>Row</td><td><a href="#inner">Inner link</a><button>Action</button></td></tr></tbody></table>);
    expect(screen.getByRole('columnheader',{name:/Name/})).toHaveAttribute('aria-sort','descending');
    fireEvent.click(screen.getByRole('button',{name:'Count'}));
    expect(choose).toHaveBeenCalledWith('count');
    fireEvent.click(screen.getByRole('cell',{name:'Row'}));
    expect(open).toHaveBeenCalledTimes(1);
    fireEvent.click(screen.getByRole('link'));
    fireEvent.click(screen.getByRole('button',{name:'Action'}));
    fireEvent.click(screen.getByRole('cell',{name:'Row'}),{ctrlKey:true});
    vi.spyOn(window,'getSelection').mockReturnValue({toString:()=> 'selected'} as Selection);
    fireEvent.click(screen.getByRole('cell',{name:'Row'}));
    expect(open).toHaveBeenCalledTimes(1);
  });
});

it('holds one loader for an entire cursor traversal and discards snapshots from another scope', async () => {
  let resolveFirst!: (value: {items: Item[]; next_cursor: string}) => void;
  const firstPage = new Promise<{items: Item[]; next_cursor: string}>(resolve => {resolveFirst=resolve;});
  const oldLoader = vi.fn((cursor: string) => cursor ? Promise.resolve({items:[{id:'b',name:'Alpha',count:10}],next_cursor:''}) : firstPage);
  const newLoader = vi.fn(async () => ({items:[{id:'c',name:'Other user',count:1}],next_cursor:''}));
  const {result,rerender} = renderHook(({loadPage,scope}) => useResourceTable({items,loadPage,pageSize:10,scope}),{initialProps:{loadPage:oldLoader as (cursor:string,signal:AbortSignal)=>Promise<{items:Item[];next_cursor:string}>,scope:'first'}});
  act(() => result.current.choose('name'));
  rerender({loadPage:newLoader,scope:'first'});
  await act(async () => resolveFirst({items,next_cursor:'second'}));
  await waitFor(() => expect(result.current.rows(columns).total).toBe(2));
  expect(newLoader).not.toHaveBeenCalled();
  expect(oldLoader.mock.calls.map(call=>call[0])).toEqual(['','second']);
  rerender({loadPage:newLoader,scope:'second'});
  expect(result.current.sourceItems.some(item=>item.name==='Alpha')).toBe(false);
  await waitFor(() => expect(result.current.rows(columns).items[0]?.name).toBe('Other user'));
});


it.each([{status:401},{status:403},{code:'unauthenticated'},{code:'forbidden'}])('clears a completed sorted snapshot when access is rejected: %j', async rejected => {
  const loadPage = vi.fn(async () => ({items,next_cursor:''}));
  const {result} = renderHook(() => useResourceTable({items,loadPage,pageSize:10}));
  act(() => result.current.choose('name'));
  await waitFor(() => expect(result.current.busy).toBe(false));
  expect(result.current.rows(columns).total).toBe(1);
  loadPage.mockRejectedValue(Object.assign(new Error('Access rejected'),rejected));
  act(() => result.current.refresh());
  await waitFor(() => expect(result.current.error).toBe('Access rejected'));
  expect(result.current.rows(columns).total).toBe(0);
  expect(result.current.sourceItems).toEqual([]);
  act(() => result.current.choose(''));
  expect(result.current.rows(columns).total).toBe(0);
  expect(result.current.sourceItems).toEqual([]);
  expect(result.current.error).toBe('Access rejected');
});

it('identifies retained complete records when a refresh reaches its deadline', async () => {
  const loadPage=vi.fn(async()=>({items,next_cursor:''}));
  const {result,unmount}=renderHook(()=>useResourceTable({items,loadPage,pageSize:10}));
  act(()=>result.current.choose('name'));
  await waitFor(()=>expect(result.current.busy).toBe(false));
  vi.useFakeTimers();
  try {
    loadPage.mockImplementation(()=>new Promise(()=>{}));
    act(()=>result.current.refresh());
    act(()=>vi.advanceTimersByTime(30_000));
    expect(result.current.error).toContain('Showing the last complete result');
    expect(result.current.error).toContain('Sorting took too long');
    expect(result.current.busy).toBe(false);
    expect(result.current.rows(columns).items).toEqual(items);
    unmount();
  } finally {vi.useRealTimers();}
});

it('fences parent reads and aborts a pending sorted load after an external denial', async () => {
  let resolve!: (value:{items:Item[];next_cursor:string})=>void;
  let signal:AbortSignal|undefined;
  const loadPage=vi.fn(async (_cursor:string,requestSignal:AbortSignal)=>{signal=requestSignal;return {items,next_cursor:''};});
  const {result}=renderHook(()=>useResourceTable({items,loadPage,pageSize:10}));
  act(()=>result.current.choose('name'));
  await waitFor(()=>expect(result.current.busy).toBe(false));
  const acceptsOldRead=result.current.readGuard();
  loadPage.mockImplementation((_cursor,requestSignal)=>{signal=requestSignal;return new Promise(done=>{resolve=done;});});
  act(()=>result.current.refresh());
  act(()=>{result.current.readFailed(Object.assign(new Error('Access rejected'),{status:403}));});
  expect(acceptsOldRead()).toBe(false);
  expect(signal?.aborted).toBe(true);
  expect(result.current.rows(columns).total).toBe(0);
  await act(async()=>resolve({items,next_cursor:''}));
  expect(result.current.rows(columns).total).toBe(0);
  expect(result.current.error).toBe('Access rejected');
  expect(result.current.busy).toBe(false);
  expect(result.current.readGuard()()).toBe(true);
});

it('keeps rejected base records hidden across query changes until a fresh parent read is confirmed', async () => {
  const loadPage=async()=>({items,next_cursor:''});
  const {result,rerender}=renderHook(({scope,records})=>useResourceTable({items:records,loadPage,pageSize:10,scope}),{initialProps:{scope:'first-query',records:items}});
  act(()=>result.current.choose('name'));
  await waitFor(()=>expect(result.current.busy).toBe(false));
  act(()=>{result.current.readFailed(Object.assign(new Error('Access rejected'),{status:403}));result.current.choose('');});
  rerender({scope:'another-query',records:items});
  expect(result.current.rows(columns).items).toEqual([]);
  expect(result.current.error).toBe('Access rejected');
  rerender({scope:'another-query',records:[...items]});
  expect(result.current.rows(columns).items).toEqual(items);
  expect(result.current.error).toBe('');
});
