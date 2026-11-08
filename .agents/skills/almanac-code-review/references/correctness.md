# Correctness and failure handling

Use for changed executable behavior, including shell commands, configuration and
documented operational steps that a user would actually run.

- Trace ordinary inputs, empty values, bounds, partial completion and repeated
  requests through the changed path. Find the reachable wrong result, not merely
  a suspicious expression.
- Follow errors to the API, CLI, UI or operator. An ignored Go error, successful
  shell exit after a failed producer, or fallback that replaces failure with empty
  data can create a false success. Verify whether the failure is intentionally
  tolerated and still observable.
- Check cancellation through I/O, bounded goroutines and retries, ownership of
  channels, cleanup on early return, and shutdown while work is in flight.
- Construct concrete concurrent interleavings and partial failures between side
  effects. Check replay after a lost response without assuming exactly-once effects.
- Read `go.mod` and available repository Go guidance before language/version-specific
  claims. If evidence is unavailable, disclose the limitation rather than invoking
  generation or execution commands during review.
- Keep validation and error handling when code is moved or consolidated. Identify
  an actual consumer before proposing a new interface, abstraction or dependency.

For a finding, name the input/state, call path, incorrect outcome and smallest
correction. For a resource issue, show who owns the resource and which exit leaks it.
