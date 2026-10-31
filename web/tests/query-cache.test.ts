import { expect, it, vi } from 'vitest';
import { clearQueryCache, sharedRead } from '../src/queryCache.ts';

it('shares an in-flight read without letting one caller cancel another', async () => {
  let complete!: (value: string) => void;
  let requestSignal!: AbortSignal;
  const load = vi.fn((signal: AbortSignal) => { requestSignal = signal; return new Promise<string>(resolve => { complete = resolve; }); });
  const first = new AbortController(), second = new AbortController();
  const a = sharedRead('shared', load, first.signal);
  const b = sharedRead('shared', load, second.signal);
  first.abort();
  await expect(a).rejects.toHaveProperty('name', 'AbortError');
  expect(requestSignal.aborted).toBe(false);
  complete('confirmed');
  await expect(b).resolves.toBe('confirmed');
  await expect(sharedRead('shared', load)).resolves.toBe('confirmed');
  expect(load).toHaveBeenCalledTimes(1);
});

it('clears protected results and rejects late completion after the session changes', async () => {
  let complete!: (value: string) => void;
  const old = sharedRead('protected', () => new Promise<string>(resolve => { complete = resolve; }));
  clearQueryCache();
  await expect(old).rejects.toHaveProperty('name', 'AbortError');
  complete('old user data');
  await expect(sharedRead('protected', async () => 'new user data')).resolves.toBe('new user data');
});

it('expires confirmed data and never caches failed validation', async () => {
  const clock = vi.spyOn(Date, 'now').mockReturnValue(1000);
  try {
    const load = vi.fn(async () => 'value');
    await sharedRead('expiring', load);
    clock.mockReturnValue(31_001);
    await sharedRead('expiring', load);
    expect(load).toHaveBeenCalledTimes(2);
    const invalid = vi.fn().mockRejectedValueOnce(new Error('invalid response')).mockResolvedValue('valid');
    await expect(sharedRead('validated', invalid)).rejects.toThrow('invalid response');
    await expect(sharedRead('validated', invalid)).resolves.toBe('valid');
  } finally { clock.mockRestore(); }
});

it('reports a synchronous loader failure as a rejected read without poisoning its key', async () => {
  await expect(sharedRead('synchronous-failure', () => { throw new Error('invalid input'); })).rejects.toThrow('invalid input');
  await expect(sharedRead('synchronous-failure', async () => 'corrected')).resolves.toBe('corrected');
});
