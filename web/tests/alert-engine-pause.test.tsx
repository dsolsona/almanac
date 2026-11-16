import {afterEach, beforeEach, expect, it, vi} from 'vitest';
import {pauseSourceBeforeRollover} from '../perf/alert-engine.pause.ts';

const rollover = Date.parse('2026-11-16T23:00:00Z');
let controller: AbortController;
beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(rollover - 120_000);
  controller = new AbortController();
});
afterEach(() => {
  controller.abort();
  vi.clearAllTimers();
  vi.useRealTimers();
});

it('requests before rollover and waits for the completed-turn acknowledgement', async () => {
  const requests: number[] = [];
  let acknowledged = false;
  const pending = pauseSourceBeforeRollover(rollover, controller.signal, () => requests.push(Date.now()), () => acknowledged);
  void pending.catch(() => {});
  await vi.advanceTimersByTimeAsync(60_000);
  expect(requests).toEqual([rollover - 60_000]);
  let finished = false;
  void pending.then(() => {finished = true;}, () => {});
  await vi.advanceTimersByTimeAsync(1000);
  expect(finished).toBe(false);
  acknowledged = true;
  await vi.advanceTimersByTimeAsync(1000);
  expect(await pending).toEqual({requestedAt: '2026-11-16T22:59:00.000Z', acknowledgedAt: '2026-11-16T22:59:02.000Z'});
});

it('rejects a timer delayed past rollover without installing a late marker', async () => {
  const request = vi.fn();
  const pending = pauseSourceBeforeRollover(rollover, controller.signal, request, () => true);
  const outcome = pending.catch(error => error);
  vi.setSystemTime(rollover);
  await vi.advanceTimersByTimeAsync(60_000);
  expect(await outcome).toEqual(new Error('source_pause_missed_rollover'));
  expect(request).not.toHaveBeenCalled();
});

it('rejects acknowledgement checks that finish after rollover', async () => {
  vi.setSystemTime(rollover - 60_000);
  const outcome = pauseSourceBeforeRollover(rollover, controller.signal, () => {}, () => {
    vi.setSystemTime(rollover);
    return true;
  }).catch(error => error);
  expect(await outcome).toEqual(new Error('source_pause_missed_rollover'));
});

it('fails within the existing acknowledgement budget when the worker never pauses', async () => {
  vi.setSystemTime(rollover - 60_000);
  const outcome = pauseSourceBeforeRollover(rollover, controller.signal, () => {}, () => false).catch(error => error);
  await vi.advanceTimersByTimeAsync(30_000);
  expect(await outcome).toEqual(new Error('source_pause_acknowledgement_timeout'));
  expect(Date.now()).toBe(rollover - 30_000);
});

it('cancels a future pause without installing its marker or leaving a timer', async () => {
  const request = vi.fn();
  const outcome = pauseSourceBeforeRollover(rollover, controller.signal, request, () => true).catch(error => error);
  controller.abort();
  await outcome;
  expect(request).not.toHaveBeenCalled();
  expect(vi.getTimerCount()).toBe(0);
});
