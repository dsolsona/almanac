# Proposed ways of working

Status: draft for maintainer review. Product constraints in `AGENTS.md` reflect
the project brief; the workflow details and delivery order below are proposals.
This document does not accept backend architecture or claim implemented features.
The current product requirements and open decisions are collected in
[SPEC-0001](specs/0001-system-overview.md). Read it before using the delivery sketch.

## Objective

Deliver a usable single-tenant Go application quickly while making its behavior
and important technical decisions explicit. Preserve the Almanac visual design.
Support SLI collection from disparate sources, SLO measurement, error budgets,
burn rates, reporting, and alerting.

## Delivery approach

Recommended: deliver small complete features, each supported by a behavior spec,
the relevant ADRs, an implementation, and acceptance evidence. Review available
components for reuse within each feature, including their tests and dependencies.
Adapt only what the feature needs. Maintain a runnable application after each
delivery stage once the foundation exists.

Write each decision in terms of Almanac's requirements, alternatives, and
consequences. Keep project documentation focused on enduring guidance.

## The feature loop

1. **Inspect.** Read relevant code and tests. Identify useful components, hidden
   dependencies, correctness assumptions, and missing coverage. Investigate only
   as far as needed for the current feature.
2. **Specify.** Write the observable contract, scope, non-goals, failure behavior,
   and numbered acceptance criteria. Include concrete examples for calculations.
3. **Decide.** Link existing accepted ADRs. Propose new ADRs only for consequential
   choices. The maintainer accepts new specifications and architectural decisions
   before their implementation; routine work within them needs no new approval.
4. **Plan.** For multi-step work, list small tasks and dependencies, each linked
   to acceptance criteria. Keep a short checklist in the spec for simple work.
5. **Implement.** Reuse reviewed components and tests when they fit. Keep only
   dependencies needed by the accepted scope. Update source contracts before
   generated implementations when generation is part of the chosen architecture.
6. **Verify.** Exercise the acceptance criteria, including failures and recovery.
   Validate reused behavior here. Record exact commands, results, and unresolved
   gaps; include visual evidence for UI changes.
7. **Reconcile.** Update the spec and delivery status. Report what works, what is
   deferred, and any decisions needed next. Publish only when authorized.

A defect that restores specified behavior needs a regression check and a fix;
it does not need a new architectural decision. Documentation-only edits need
content and link checks, not an application test run.

## Documentation structure

Create documents as needed, rather than scaffolding all future features.

| Location | Purpose |
| --- | --- |
| `AGENTS.md` | Concise contributor and agent instructions |
| `docs/specs/NNNN-topic.md` | Current behavior contract and acceptance criteria |
| `docs/adr/NNNN-decision.md` | Why a consequential choice was made |
| `docs/plans/NNNN-topic.md` | Optional task breakdown linked to a spec |
| `docs/status.md` | Delivery stages, implementation status, verification evidence |
| `web/DESIGN_SYSTEM.md` | Visual rules and links to actual shared assets, once established |

Specs use `draft`, `accepted`, and `superseded` states. Track implementation
separately as `not started`, `in progress`, or `verified`, with evidence and known
limitations. An accepted spec is not proof that its behavior exists.

ADRs use `proposed`, `accepted`, `rejected`, and `superseded` states. Record the
acceptance date and maintainer approval reference. Preserve accepted rationale;
superseding records explain what changed and why. Correcting a typo or link does
not require a new decision.

## Minimum spec contents

- Identifier, title, status, and purpose.
- User-visible behavior, scope, and explicit non-goals.
- Inputs, outputs, data semantics, and relevant API or UI contracts.
- Failure, recovery, and operational behavior appropriate to the feature.
- Numbered acceptance criteria with concrete examples.
- Linked ADRs, dependencies, and unresolved questions.
- Implementation status and acceptance evidence.

## Minimum ADR contents

- Identifier, title, status, date, and approval reference when accepted.
- Context: requirements and constraints driving the choice.
- Decision: what Almanac will do and the boundary of that decision.
- Alternatives: credible choices and why they were not selected.
- Consequences: benefits, costs, operational effects, and limitations.
- Links to affected specs and any superseded decision.

Create ADRs for choices that shape correctness, public contracts, security,
operations, dependencies, or several features. Avoid splitting one coherent
decision into many records just to document every implementation detail.

## Delivery and current decisions

The proposed delivery sequence, release boundaries, and verification expectations
are maintained in [SPEC-0014](specs/0014-delivery-and-verification.md). Current
acceptance and implementation status is in [status.md](status.md). Review the
[design guide](review-guide.md) for proposed choices and unresolved product questions.

Keep a single source for detailed product behavior. This document describes the
working method; it does not override accepted specs or ADRs and does not maintain
a separate roadmap with conflicting assumptions.
