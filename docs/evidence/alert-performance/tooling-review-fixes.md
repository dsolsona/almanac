# Consolidation review: diagnostic tooling corrections

Base: `88c4461335b9489875b1b1779b47ca8d0b9fd382`. These corrections follow the
completed review and explicit authorization to fix its valid findings. They do
not change application behavior, performance budgets, workload size or retries.
No application fixture, E2E workload, remote host or performance control was run.

## Corrections and evidence boundaries

- [PR115 certificate verification](https://github.com/dsolsona/almanac/pull/115#discussion_r4574082711)
  and [credential redirects](https://github.com/dsolsona/almanac/pull/115#discussion_r4574082721):
  the standalone navigation client no longer ignores HTTPS errors. Both browser
  and API verify TLS. Its protocol rejects non-Secure cookies; API redirects are
  disabled. Ordinary Almanac session cookies already carry Secure. Optional
  self-signed fixture trust uses a single public certificate supplied through
  `NODE_EXTRA_CA_CERTS` **before Node starts**, with hostname/validity checks and
  its derived SPKI scoped to the disposable Chromium process. Details and the
  trusted-channel requirement are in [F4-T21](../../tasks/F4-T21.md). Authentication
  remains enabled. Normal browser redirects retain ordinary TLS/domain/Secure
  cookie behavior; no blanket origin-routing guarantee is claimed.
- [PR117 timeout cleanup](https://github.com/dsolsona/almanac/pull/117#discussion_r4574081308):
  killing an owned process group no longer depends on its leader remaining alive.
  A direct child can exit on TERM while a descendant ignores it; the final group
  KILL now still runs, followed by bounded direct-child reaping.
- [PR117 incomplete measurements](https://github.com/dsolsona/almanac/pull/117#discussion_r4574081318):
  ingestion-only, loaded ingestion, due coverage and live-arrival fields must all
  exist with numeric counts. Explicit null histogram bounds/maximum receipt delay
  retain unavailable evidence. Missing fields fail completeness; poor latency,
  unsuccessful requests and missing intervals remain in artifacts. Completeness
  is not performance acceptance, and the diagnostic still sets capacity acceptance
  false.
- [PR98 final capture cleanup](https://github.com/dsolsona/almanac/pull/98#discussion_r4568029388):
  a failure in final resource or ingestion collection now stops and joins API
  workers through `finally`, preserving the original capture failure. Workers'
  `allSettled` observer remains attached from their start; minimum cycles and
  bounded requests are unchanged.
- [PR95 fallback counter](https://github.com/dsolsona/almanac/pull/95#discussion_r4568032880):
  only a prepared candidate row with dirty interior hours counts as a successful
  substitution. Dirty refused rows no longer satisfy the exercise assertion.
  Existing independent paired exactness checks remain unchanged. Earlier values
  of this counter alone cannot prove a successful substitution.
- [PR97 punctuality claim](https://github.com/dsolsona/almanac/pull/97#pullrequestreview-5897230781):
  the due-interval query establishes presence at phase end, not arrival by each
  interval's individual deadline. Its assertion and [F4-T03](../../tasks/F4-T03.md)
  now say that explicitly. No per-interval punctuality evidence is invented and
  the accepted delivery deadlines remain unchanged.

## Focused verification

```sh
make shell CMD='python3 scripts/alert-cpu-control-test.py && python3 -m py_compile scripts/alert-cpu-control.py scripts/alert-cpu-control-test.py'
make shell CMD='cd web && npm test -- tests/alert-navigation-tls.test.tsx tests/alert-navigation.test.tsx tests/alert-calculation-experiment.test.tsx && npm run typecheck'
make shell CMD='python3 scripts/check-docs.py'
```

Python: 25 checks pass in **0.635 s**, syntax passes, command **0.977 s**. The
pre-fix cases fail for all nineteen omitted measurement fields and the surviving
TERM-ignoring descendant: 20 failures in 1.659 s. The descendant test uses real
isolated processes and always kills its owned child on failure; it touches no
application container. Existing sixteen CPU-control checks remain included.

Web: **43 checks pass in 3.14 s**, TypeScript passes, combined command **5.402 s**.
The TLS tests create ephemeral certificates and loopback servers on OS-assigned
ports, then exercise actual Chromium and Playwright API requests in new Node
processes. The selected certificate carries authenticated requests; no trust or
an unrelated certificate yields no HTTP request; an API downgrade returns 302
without following it. Wrong-host fixture trust is rejected before clients launch.
No catalogue server or port 5200 is used.

Documentation checks pass for **273 Markdown files / 2,093 local links** in
**0.636 s**. `git diff --check` passes.

Failure history: three new helper checks first fail because the missing exported
helpers cannot supply the required behavior (0.817 s); insecure-cookie validation
then fails with 38 related checks passing (1.62 s). Initial TLS tests fail because
the new transport module is absent (0.833 s). After implementation the original
42 checks pass (3.38 s). An additional browser-origin restriction check fails
(3.15 s): Playwright route handlers do not re-intercept redirected requests. That
unrequested, ineffective routing layer was removed; it is not claimed as fixed.
Required TLS and API-redirect tests remain and pass on the final candidate.

No integrated remote runner, full benchmark or normal-container journey was run
for these tooling changes. The primary agent owns integrated review and any
follow-up verification. Existing failures and capacity gaps remain open.
