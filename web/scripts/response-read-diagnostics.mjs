export async function readResponseJSON(response) {
  try {
    return await response.json();
  } catch (error) {
    const message = typeof error?.message === 'string' ? error.message : '';
    const category = error instanceof SyntaxError ? 'body_json_invalid'
      : message.includes('Protocol error (Network.getResponseBody): No resource with given identifier found') ? 'body_resource_missing'
        : message.includes('Protocol error (Network.getResponseBody): Request content was evicted from inspector cache') ? 'body_cache_evicted'
          : 'body_read_failed';
    const safeError = new Error('Response JSON read failed');
    safeError.name = 'ResponseReadError';
    safeError.category = category;
    throw safeError;
  }
}
