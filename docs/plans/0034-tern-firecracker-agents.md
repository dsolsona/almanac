# Tern Firecracker agents implementation plan

**Goal:** `make vm-run` starts a named worker and Codex directly in guest Tern,
then connects the laptop's existing Tern window without manual host setup.

**Architecture:** Extend the existing launcher under
[ADR-0048](../adr/0048-tern-firecracker-agent-terminals.md). Reuse prepared disks,
SSH identity verification, prompt transport and local Tern control APIs. The
primary agent implements and verifies inline in `.worktrees/firecracker-terminal`;
the maintainer authorized committing and opening a PR after the bare-command
workflow is complete. Merge is not authorized.

## Scope and interfaces

- `scripts/firecracker-setup.sh`: preflight, guest services/runtime, direct session
  launch, reconnect, safe task inspection and worker-owned local identity cleanup.
- `scripts/firecracker-tern.py`: local window connection using existing Tern
  APIs, bounded waits and private per-request result files.
- Focused launcher/controller tests: fake external boundaries, real parsers,
  prompt bytes and error handling; live native-Tern checks supplement fixtures.
- Make help, command/development guides and F1-T21 record: describe the actual
  entry points, prerequisites, recovery and measured acceptance.

## Steps

- [x] Add failing checks for Tern launch/connection, duplicate-task refusal,
  unavailable local Tern and authentication, connect failure and legacy tmux.
- [x] Implement guest service setup and direct Codex launch. Retain prompts on
  inspection failure and prevent replay of the initial prompt after reboot.
- [x] Implement local trust/add/switch and recovery through `vm-codex`.
- [x] Connect collection/base/deletion guards to the actual session backend;
  update documentation and Make help in the same candidate.
- [x] Share primary-checkout VM state across linked worktrees, keeping the current
  checkout as the source so bare Make commands need no state override or `-C`.
- [x] Run focused container checks, syntax/link checks and a real guest trial;
  inspect the complete diff and record results and remaining gaps.

## Review focus and acceptance

Fresh and existing prepared workers, two simultaneous workers, dirty source
snapshots, whitespace/metacharacter prompts, active/exited sessions, unknown
remote state, stale fingerprint, unavailable GUI/key and connection interruption.
No private SSH key, prompt or login may enter a command log or reusable base.
Runtime additions must not trigger another application `make setup`. Closing the
client must preserve Codex; worker stop/start must not replay its original task.
Collection must refuse a live agent. Deletion must preserve other workers and
unrelated local Tern registrations. A client-connect error must retain the agent
and give a working reconnect command.

Focused command: `make shell CMD='python3 scripts/firecracker-setup-test.py && python3 scripts/firecracker-tern-test.py && python3 scripts/make-help-test.py && python3 scripts/check-docs.py'`.
Host checks: `bash -n scripts/firecracker-setup.sh` and `git diff --check`.
No application suite is required for this development-tooling-only change.

Live evidence and failure history are recorded in [F1-T21](../tasks/F1-T21.md).

Authentication follow-up under [ADR-0050](../adr/0050-dedicated-firecracker-tern-client-key.md):
replace personal-key discovery with a serialized dedicated-key setup and scoped
SSH include; verify first-use creation, reuse, concurrent launches, preservation
of other SSH settings and refusal to replace an unsafe existing key. Run focused
launcher/Tern/docs checks, then reconnect the existing reported worker without
changing its Codex process or task. Keep this follow-up in
`.worktrees/firecracker-tern-auth` on `fix/firecracker-tern-auth`.

Stencil Tern 0.9.16 was verified with one laptop window and two native workers.
Multiple laptop windows, runtime upgrades to another Tern version and the local
GUI-start path when no window exists remain unverified. Legacy tmux input repair
was confirmed by the maintainer; physical keyboard input through native Tern
was not separately exercised in this follow-up.
