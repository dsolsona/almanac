# ADR-0008: Runtime components and packaging

- Status: accepted
- Created: 2026-10-24
- Accepted: 2026-10-24
- Approval: Crit review `7fd22977b6c0`; approved 2026-10-24 after all comments resolved
- Related spec: [SPEC-0013](../specs/0013-runtime-and-operations.md)

## Context and decision

Almanac needs easy local operation and a route to independent component deployment.
Propose one Go module, an `almanac` executable for server component modes and CLI,
and a separate `almanac-agent` executable. The UI is a React application running
in the user's browser and communicating with the backend through HTTP APIs.

“Embed built UI assets” describes distribution only: the frontend build produces
HTML, JavaScript and CSS files, and those files are packaged inside the Go executable.
The UI component serves those files to the browser, just as a static web server would.
Go does not render React screens or replace the browser-to-API architecture. This
lets a user download one server binary without installing Node or copying a separate
web directory. The UI-only mode serves the same React build and proxies API requests
to a separately running backend; it needs no database access.

Default server mode combines backend/UI/alerts; component selection separates them.
No combined server mode includes external collection.

Only the backend accesses PostgreSQL. Separate alert workers consume private APIs
for claims and atomic state transitions. UI instances serve static assets plus a
fixed backend proxy. Shared application operations preserve combined/remote parity.
The backend owns agent connections and durable planning initially.

Start with one active backend, independently runnable UI and leased alert workers,
and many individually registered agents. Preserve service/adapter and dispatch
boundaries for future backend replication; do not add a broker before requirements
and measured workloads justify it.

## Alternatives

- A single universal executable including the agent simplifies downloads but sends
  server/UI dependencies to every collector; defer unless requested at review.
- A different project and deployment for every worker increases release/configuration
  work before the first useful system exists.
- Giving every component database credentials simplifies some calls but bypasses
  authorization and transaction invariants across independent writers.
- Building a distributed connection registry immediately makes independent scaling
  more complete but adds failure modes without first-release evidence that it is needed.

## Consequences and review

The maintainer agreed to try packaging the static React files in the server binary,
with binary size measured before release (2026-10-24 review). This confirms that
packaging choice; the completed review subsequently accepted the full runtime ADR.
Record the server executable size with and without UI assets, the frontend build
size, and compressed distribution/image size for the same target and build flags.
Report the measured difference; no binary-size claim exists before a build. If the
added size is unacceptable in review, revisit a separately shipped static bundle.

API contracts must support internal worker operations, claims and fencing. Database
ownership remains clear, while the backend is initially a single availability unit.
Combined mode must exercise the same operations as remote workers. No Node production
runtime. Confirm artifact split and the initial scale limit in **REVIEW-R01/R02**.
Revisit backend replication after capacity/availability needs are demonstrated.
