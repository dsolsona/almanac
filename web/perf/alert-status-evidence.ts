// Calculation state is evidence, not an HTTP-success or ready-only acceptance gate.
// Compatible stale/failed values and unavailable values are valid SPEC-0019 responses.
export function calculationEvidence(body: unknown) {
  const value = body !== null && typeof body === 'object' ? body as Record<string, unknown> : {};
  const generation = value.generation !== null && typeof value.generation === 'object' ? value.generation as Record<string, unknown> : {};
  const state = value.current_state;
  const evaluated = generation.evaluated_through;
  const lag = generation.calculation_lag_seconds;
  return {
    currentState: typeof state === 'string' && ['ready','stale','failed','unavailable'].includes(state) ? state : null,
    evaluatedThrough: typeof evaluated === 'string' && /^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d{1,9})?Z$/.test(evaluated) && Number.isFinite(Date.parse(evaluated)) ? evaluated : null,
    calculationLagSeconds: typeof lag === 'number' && Number.isFinite(lag) && lag >= 0 ? lag : null,
  };
}
