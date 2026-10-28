export function readResponseJSON(response: { json(): Promise<unknown> }): Promise<unknown>;
