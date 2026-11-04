import { describe, expect, it } from 'vitest';
import { captureResponseJSON, readResponseJSON } from '../scripts/response-read-diagnostics.mjs';

describe('browser response read diagnostics', () => {
  it('captures the body while the click is pending, before its response cache disappears', async () => {
    const fixture = () => {
      let available = true;
      const response = { json: async () => {
        if (!available) throw new Error('Protocol error (Network.getResponseBody): No resource with given identifier found');
        return { id: 'receipt' };
      } };
      const click = async () => { await Promise.resolve(); available = false; };
      return { response, click };
    };
    const late = fixture();
    await Promise.all([Promise.resolve(late.response), late.click()]);
    await expect(readResponseJSON(late.response)).rejects.toMatchObject({ category: 'body_resource_missing' });
    const early = fixture();
    await expect(captureResponseJSON(Promise.resolve(early.response), early.click))
      .resolves.toEqual({ response: early.response, body: { id: 'receipt' } });
  });
  it('returns successful JSON unchanged', async () => {
    const value = { version: '2' };
    await expect(readResponseJSON({ json: async () => value })).resolves.toBe(value);
  });

  it.each([
    [new SyntaxError('unexpected secret-token'), 'body_json_invalid'],
    [new Error('response.json: Protocol error (Network.getResponseBody): No resource with given identifier found'), 'body_resource_missing'],
    [new Error('response.body: Protocol error (Network.getResponseBody): Request content was evicted from inspector cache'), 'body_cache_evicted'],
    [new Error('arbitrary private secret-token'), 'body_read_failed'],
  ])('reports only the fixed category for a rejected read', async (failure, category) => {
    try {
      await readResponseJSON({ json: async () => { throw failure; } });
      throw new Error('expected rejection');
    } catch (error) {
      expect(error).toMatchObject({ name: 'ResponseReadError', category });
      expect(String(error)).not.toContain('secret-token');
      expect(error).not.toHaveProperty('cause');
    }
  });
});
