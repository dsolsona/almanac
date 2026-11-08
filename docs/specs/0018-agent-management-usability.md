# SPEC-0018: Usable agent startup and management

- Status: accepted
- Created: 2026-10-28
- Accepted: 2026-10-28
- Approval: The maintainer approved the four numbered requirements in the working conversation: credential-driven Make startup, dependency-blocked deletion, readable durations and no repeated password prompts.
- Implementation: in progress
- Verification: Immediate reassignment is tracked in [F2-T33](../tasks/F2-T33.md); earlier management evidence is in [F2-T24](../tasks/F2-T24.md) through [F2-T27](../tasks/F2-T27.md) record the management increment; [F2-T31](../tasks/F2-T31.md) records verified immediate restart and crash recovery
- Related ADRs: [ADR-0020](../adr/0020-session-authorized-management.md)
- Plan: [agent management](../plans/0020-agent-management.md)
- Supersedes / superseded by: Amends [SPEC-0004](0004-identity-and-access.md), [SPEC-0010](0010-api-and-cli.md), [SPEC-0012](0012-user-interface.md) and [SPEC-0013](0013-runtime-and-operations.md) for these workflows.

## Purpose and scope

Let an administrator register an agent, start it locally with its issued credential,
assign a datasource, manage credentials and retire an unassigned agent without
repeat password entry or unexplained transport units. Use existing shared controls,
themes, identity and provider contracts. This does not add automatic registration,
production orchestration, provider types or new personal-token permissions.

## Local launcher

Provide `make agent` for the running local Compose application. Prompt privately
for the supplied agent credential; also accept a protected file through
`AGENT_TOKEN_FILE` for noninteractive use. Never put a token in command arguments,
logs or Docker environment metadata. Run the packaged `almanac-agent` in a separate
container on the local application network, verify backend TLS with the public CA,
and use the operator's selected Compose project. Do not restart the app, mutate its
users or register identities. The command remains attached and Ctrl-C stops its
owned process/container. Fail clearly for missing prerequisites, unsafe files,
invalid credentials and fenced sessions, with safe guidance. Keep temporary
credentials private and clean them on failure or interruption. Under
[ADR-0022](../adr/0022-restartable-agent-ownership.md), the same command immediately
replaces a stopped, crashed or still-running prior process using the supplied valid
credential. A replaced process reports replacement and exits; it does not try to
take ownership back. Do not report container startup as proof of connection. The
UI's agent status supplies that proof.

## Agent deletion

An administrator may delete only an agent with no currently assigned datasources,
including disabled datasource assignments. An attempt while assigned must not revoke
tokens or change the agent. The dialog explains why, lists linked datasource names,
directs the user to update their assignments and offers Register replacement agent.
If dependencies cannot be read, show an error and retry, never an empty dependency
list or enabled destructive confirmation based on missing data.

Once unassigned, explicit confirmation deletes the agent, revokes every credential
and returns to the agent list. Preserve historical observations, audit and identity;
hide the deleted identity from normal lists and selectors and reject future reads,
assignment, credential issuance and connection using it. Preserve name uniqueness.
Use a version precondition, idempotency key and current administrator authority.
The transaction must serialize datasource assignment/reassignment with deletion so
an assigned deleted agent is impossible. API and CLI expose the same result and
dependency information; paginated dependencies remain discoverable and bounded.

`GET /api/v1/agents/{agent_id}/datasources` exposes cursor-paginated dependency
summaries with `id`, `name` and `display_name`, including disabled assignments.
`DELETE /api/v1/agents/{agent_id}` requires `If-Match`, `Idempotency-Key` and an
explicit empty JSON object body, and returns a replayable lifecycle receipt.
The body lets clients retain the existing no-automatic-mutation-replay transport
boundary; receipt replay remains an explicit caller action.
An assigned agent returns HTTP 409 with
a stable dependency-conflict code distinct from a stale-version conflict. The
client reloads the dependency list on that conflict. Successful receipt replay
continues to work after archival, subject to current authorization.

## Credential controls and session authority

Use "New credential expires after" and "Keep the previous credential valid for"
as preset chips with a self-applying "Custom…" chip (no unit selector): expiry
presets 30 days, 90 days and 1 year with custom whole days; grace presets
Immediately, 1, 5 and 10 minutes with custom whole seconds. Defaults are 90 days
and 10 minutes, with short explanations. An invalid custom value is announced
once, at the field.
Registration uses the same expiry control. Explain that expiry stops authentication
and the grace period allows updating the running agent. Preserve maximum lifetime
of one year, maximum overlap of ten minutes, explicit immediate retirement and
exact numeric API semantics; custom values must not silently round or truncate.
Retain user input on errors and show understandable bounds. Retry summaries show
the same readable values as the submitted command.

Signed-in management uses current session/role without old-password fields,
password-file flags or recent-login requirements. Apply to existing UI/API/CLI
agent lifecycle and password-change/onboarding flows. Keep sign-in and new-password
entry, authorization, CSRF, audit, idempotency and one-time secret behavior.
Expired sessions require sign-in; they do not invite password proof in a dialog.

## Acceptance criteria

- AC-01: A registered credential supplied to `make agent` connects a separate real
  container agent; a datasource assigned to it can test/query and collect an SLI.
  Immediate clean/crash replacement works before prior lease expiry and fences old
  results. Secrets do not appear in command lines, logs or saved screenshots.
- AC-02: Assigned agents cannot be deleted; linked dependencies and a replacement
  registration path are visible. Reassigning all dependencies permits deletion.
- AC-03: Concurrent assignment/delete cannot create a dangling active assignment;
  deletion revokes all tokens and preserves history. Stale versions, lost responses
  and receipt replay preserve existing authority/idempotency guarantees.
- AC-04: Expiry/grace controls use readable values in light/dark and mobile layouts;
  ordinary defaults, custom values, zero overlap and invalid bounds work consistently.
- AC-05: Authorized session actions succeed without old-password proof across UI,
  API and CLI; restricted, expired, revoked and non-admin sessions remain rejected
  where appropriate. Sign-in and new-password policy remain enforced.
- AC-06: Real Compose user journeys exercise startup, credential lifecycle, blocked
  deletion, reassignment and successful deletion. Unit/database/race/generated and
  browser checks verify the candidate without replacing real acceptance with mocks.

## Reassigning a datasource

Under [ADR-0037](../adr/0037-immediate-datasource-reassignment.md), an editor or
administrator can immediately select a different agent, including while a prior
move is pending. Editors can list safe agent summaries for selection and change
assignment only; datasource creation, connection settings, credentials and agent
lifecycle remain administrator-only. Viewers cannot change assignment.

Atomically change `agent_id`, partition canonical ownership intervals and publish
new configurations for both agents. The replacement does not wait for the old
agent's removal acknowledgement, reachability or lease expiry. The old agent
receives configuration without the datasource when it reconnects. Reject all its
subsequent submissions for that datasource, including buffered earlier intervals
and receipt replay; preserve already accepted history, measurement revisions and
unrelated datasource collection. Reassignment does not change query definitions
or source settling delays. Superseding a future ownership epoch must not create
zero-length epochs or revive earlier grants.

The datasource response reports the selected replacement in `agent_id` immediately.
A `reassignment` object retains source, target and request time and reports
`waiting_for_configuration`, `waiting_for_collection` or `replacement_unavailable`.
The legacy `authority_expires_at` remains for compatibility but is not a replacement
start deadline. Legacy pending moves must adopt immediate transfer without waiting
for their recorded expiry. GET requests do not mutate recovery state.

Under [ADR-0038](../adr/0038-datasource-configuration-acknowledgement.md), show
Updating until the replacement's authenticated current process acknowledges the
published configuration, then Active. Complete and audit on acknowledgement for
all datasources, without a resource version increment or another configuration
publication. Measurement freshness and query failures remain separate. Connected
v2 agents fetch and apply configuration on the WebSocket notice immediately;
normal completion takes a few seconds. Automatically refresh pending status
without overwriting unsaved edits or uncertain writes, and enable diagnostics on
configuration readiness without requiring a page reload or first observation.
If the assigned replacement is unavailable, show Not collecting and an Agent
unavailable explanation rather than an ongoing Updating label. The assignment
is already changed; configuration remains unacknowledged until the agent recovers.

Show the actual replacement blocker in UI and CLI. Operators may select another
agent if the replacement fails. Returning to the source of a still-pending
operation cancels it under [ADR-0039](../adr/0039-cancel-pending-datasource-reassignment.md):
restore assignment, publish both configurations, and end the operation immediately
without a reverse pending row or acknowledgement wait. Keep fresh ownership bounds
and authorization floors; do not restore old grants. An offline restored agent
shows Not collecting with its normal reachability/configuration state. Only actual
current assignments block deletion;
move history must not reserve a now-unassigned old agent. Serialize assignment
with agent deletion, preserve identity in audit/history and do not restore an old
assignment automatically when the replacement becomes unavailable.

- AC-07: With an old agent disconnected and its execution lease still valid, an
  editor/admin assignment immediately publishes both configurations. Old-agent
  submissions and receipt replay are rejected; Updating ends on configuration
  acknowledgement independently of measurement freshness, with diagnostics available.
- AC-08: Replacement failure, repeated moves before a canonical boundary, old-agent
  reconnect, pending-move upgrade and deletion races preserve single-owner accepted
  intervals, historical observations and unrelated collection. Editor connectivity
  edits and viewer assignment attempts are denied.

- AC-09: Canceling a pending move restores its source and publishes both configurations
  without another pending operation, even if the source is offline. Existing grants
  remain fenced. Cancellation racing with ACK returns 409 without a reverse move;
  the UI refreshes the completed assignment and removes stale rollback controls.
