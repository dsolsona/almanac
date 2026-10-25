# ADR-0015: Keep timeline annotations separate from measurement policy

- Status: accepted
- Created: 2026-10-24
- Accepted: 2026-10-24
- Approval: Crit review `0de0c0e42db6`; approved 2026-10-24 with no changes requested
- Related specs: [SPEC-0017](../specs/0017-slo-timeline-annotations.md), [SPEC-0016](../specs/0016-maintenance-exclusions.md)
- Supersedes / superseded by: none

## Context

An SLO graph is easier to interpret when a deployment or incident is visible on its
timeline. A note explaining a drop in reliability must not excuse that drop from the
calculation. Maintenance exclusions and notification silences can also appear on the
same timeline but have different operational effects and permissions.

## Decision

Store annotations as independent, versioned metadata attached to explicit SLO IDs,
with point or duration timestamps. UI and API/CLI share the same contract; scoped
automation credentials allow deployment jobs to add events. Retain audit history and
stable external event IDs so retries do not duplicate contextual events.

Chart presentation composes three distinct record types: informational annotation,
maintenance exclusion and notification silence. Rendering them together never merges
their authorization or semantics. Editing a note cannot change SLO math or mute an
alert. Operational overlays link to their owning records rather than duplicating them
as editable notes. Annotation queries and cache invalidation remain independent of
metric calculations; their outage cannot erase measured data from the screen.

Explicit saved SLO IDs keep past events stable when folders, names or agents change.
Escape text, restrict link schemes and do not fetch remote previews. Bounded paging
and visual clustering preserve frontend responsiveness and access to exact event data.

## Alternatives considered

- Store notes inside observations: couples human edits to immutable measurements
  and makes metadata changes affect calculation persistence.
- Reuse annotations for maintenance and silencing: fewer resource types, but creates
  ambiguous effects and could let a deployment-note token alter reported reliability.
- Dynamically match all past notes to today's folders/labels: convenient bulk scope,
  but changes historical context when organization changes.
- Build deployment-vendor integrations first: more setup and dependency work than a
  scoped API/CLI event can justify for the first version.

## Consequences

Operators get useful context without changing numbers. Separate APIs/data models
and audit revisions add modest persistence work; the UI needs a shared overlay model
that preserves each source type and accessibility. Retention and external event-ID
uniqueness must remain bounded and documented.

## Revisit conditions

Revisit when automatic deployment integrations, comments/attachments, dynamic scope
or longer annotation retention have concrete demand. Keep informational authority
separate from permission to alter measurement or notification policy.
