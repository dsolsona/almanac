export function readResponseJSON(response: { json(): Promise<unknown> }): Promise<unknown>;
export function captureResponseJSON<T extends { json(): Promise<unknown> }>(response: Promise<T>, action: () => Promise<unknown>): Promise<{ response: T; body: unknown }>;
