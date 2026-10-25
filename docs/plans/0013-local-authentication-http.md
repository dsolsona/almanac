# Local authentication HTTP implementation plan

> Execute with one isolated Luna implementer through subagent-driven development;
> the coordinator reviews, verifies, commits and publishes the stack without merging.

Goal: prove the protected authentication API against real TLS and PostgreSQL before
runtime and UI/CLI wiring. Governing spec: [SPEC-0004](../specs/0004-identity-and-access.md).
Task and exact interfaces: [F1-T13](../tasks/F1-T13.md).
Architecture: an HTTP adapter around the existing authentication service, generated
OpenAPI types, strict transport/origin/source checks and session-bound HMAC tokens.
Stack: Go standard library, current PostgreSQL adapter and existing generators.

## Constraints and review focus

No dependency/schema changes or runtime exposure. Never weaken cookie/TLS protections
for development. Keep session inspection non-mutating and context recovery separate
from mutation retry. Treat proxy trust, ambiguous cookies/headers, stale CSRF after
rotation/restart, idle expiry and unavailable storage as explicit acceptance cases.
Only existing approved command families; no new approval prompts. Preserve preview.

## Task 1: Protected authentication transport

- [ ] Add focused failing boundary tests and real TLS/database journey cases from F1-T13.
- [ ] Extend source contracts and regenerate Go/TypeScript definitions.
- [ ] Add non-mutating session inspection, preserving current authority/expiry rules.
- [ ] Implement the bounded handler, cookies, CSRF, strict decoding and error mapping.
- [ ] Run focused checks, serial real-database acceptance, race and integrated gate.
- [ ] Independently review actual diff and evidence; correct findings before acceptance.
- [ ] Record acceptance and publish a stacked PR; retain review evidence locally.

The next delivery wires backend startup/ownership/readiness and then the real
onboarding/account UI and setup CLI to these routes. This plan does not claim those
runtime or browser journeys are implemented.
