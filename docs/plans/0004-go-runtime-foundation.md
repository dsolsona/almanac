# Go runtime foundation implementation plan

Goal: establish a runnable Go server binary with embedded UI, fixed backend proxy,
operational health, generated contracts and bounded lifecycle.
Spec: [SPEC-0013](../specs/0013-runtime-and-operations.md),
[SPEC-0010](../specs/0010-api-and-cli.md); task [F1-T04](../tasks/F1-T04.md).

## Global constraints

Single tenant, Go1.27.3, modern Go guidance before code, no backend/DB/auth simulation.
Only UI component runs in this slice; unsupported components fail explicitly.
Generated contracts, fixed approved commands and real HTTP/lifecycle tests.

## Review focus

Proxy path/URL tricks must not reach forbidden routes or destinations. Readiness
cannot hide upstream failure. Static server cannot expose catalogue or directory
contents. Explicit false/empty config values cannot accidentally lose precedence.
Cancellation must release requests/workers within the drain bound. Generated code
must match source contracts and verify must not download missing tools.

## Task 1: Executable, embedded UI and operational boundary

Read [F1-T04](../tasks/F1-T04.md) for interfaces and acceptance.

- [x] Implement CLI/config, generated health contract, static server/proxy and lifecycle.
- [x] Verify fixed toolchain/build/test/generation lanes and executable smoke.
- [x] Review, integrate and record scope and evidence before backend persistence work.
