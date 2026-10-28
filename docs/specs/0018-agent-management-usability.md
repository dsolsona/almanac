# SPEC-0018: Usable agent startup and management

- Status: accepted
- Created: 2026-10-28
- Accepted: 2026-10-28
- Approval: The maintainer approved the four numbered requirements in the working conversation: credential-driven Make startup, dependency-blocked deletion, readable durations and no repeated password prompts.
- Implementation: verified
- Verification: [F2-T24](../tasks/F2-T24.md) through [F2-T27](../tasks/F2-T27.md) record the management increment; [F2-T31](../tasks/F2-T31.md) records verified immediate restart and crash recovery
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
with readable units, defaults of 90 days and 10 minutes, and short explanations.
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

## Reassigning an active datasource

Reassignment of a datasource with SLIs follows the normal handoff in
[SPEC-0009](0009-agent-protocol-and-ownership.md). A request reserves the old and
replacement agents, freezes renewal only of affected measurement grants, and
publishes a removal configuration to the old agent. Both agents remain deletion
blockers while the handoff is pending. The old agent acknowledges only after it has
installed that configuration and canceled and drained affected collection tasks;
otherwise its recorded authority must expire before the handoff completes. There is
no force option. Unrelated measurements continue to use their own grants.

The datasource response retains the current `agent_id` and exposes a `reassignment`
object with `source_agent_id`, `target_agent_id`, `requested_at`,
`authority_expires_at`, and `state`: `waiting_for_previous_agent` or
`replacement_unavailable`. An absent object means the assignment is active. A
bounded background reconciler completes ready handoffs without mutating GET requests.
Completion rechecks the replacement's enabled state and collection capabilities;
unavailable replacements remain reserved and visibly pending. A versioned PUT
selecting the source agent cancels a pending transition and restores its configuration
and grant renewal. Other pending edits return `reassignment_pending`. Request,
completion and cancellation are audited and advance the resource version.
For a live v2 agent, beginning its first transition and activating a replacement
wait until its current lease has caught up with its applied configuration. This
keeps the previously authorized snapshot available as the transition baseline.
If a v2 source is offline with unapplied configuration and no live ownership,
its handoff retains the old configuration and waits for authority expiry instead
of claiming a drain acknowledgement.

Handoffs retain the measurement revision and its original effective time. Separately
persisted ownership epochs partition canonical intervals at a future boundary. Old
buffered observations remain eligible only under their original grants and before
that boundary, subject to current credential and replay checks; replacement results
are eligible at or after it. Historical measurements and observations are preserved.
