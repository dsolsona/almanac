# Requirements and accepted decisions

Use the current request, relevant plan/task and accepted documents found through
the [spec index](../../../../docs/specs/README.md) and
[ADR index](../../../../docs/adr/README.md).

- Connect each promised outcome to implementation or existing behavior. Search
  before declaring something absent; a dependency increment may already supply it.
- Check that scope additions were authorized and that consequential contract or
  architecture changes have maintainer acceptance. Mechanically required callers,
  tests and generated updates are part of the intended change.
- Apply directory instructions only within their scope. Name the actual rule and
  its source when reporting a violation; distinguish a functional consequence
  from an editorial preference.
- Check the effect on existing persisted records, installed configuration and
  user workflows. A bug fix can alter previously accepted results. Describe the
  before/after behavior and any migration or compatibility obligation established
  by the accepted contract. Do not require access to a live installation.
- Keep accepted, implemented and verified states distinct. A stale progress note
  is not proof that current code is absent, complete, or measured.
- If governing contracts conflict, identify the conflict and continue independent
  review. Do not silently resolve it by choosing the implementation you prefer.

Report an unmet observable outcome or consequential rule breach. Unavailable intent
or acceptance evidence belongs in open questions, rather than an invented requirement.
