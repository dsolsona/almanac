# Settings and user administration mocks

Status: **approved design**, 2026-11-06. The maintainer explicitly approved these
mocks and requested implementation in an isolated worktree with pixel parity.
These are interactive reference
HTML mocks with fictional data, not implemented user administration. No backend,
real authentication, email delivery or credential storage is involved. Nothing is
persisted; reload restores the fixtures. Password inputs are discarded on submit
and navigation. Browser password-manager behavior is outside the mock.

Open [index.html](index.html) in a browser, or use the local preview when running.
Append `?theme=dark` for dark mode, or choose Light/Dark under Your account. Narrow
the window to 360 px to review mobile; Menu opens a native navigation dialog.

## Scope and design

Settings opens directly on **Your account**, with **Your account** and **Users**
as tabs; there is no card overview. The administrator also has a **Sessions** tab
for the inactivity setting in [ADR-0044](../../../adr/0044-configurable-session-inactivity.md). Users opens a
name-first list with separate Name and Email columns, status, role and last sign-in;
search, status and role filters;
10/20 rows per page; and one primary Add user action. Add/edit use one compact
Profile section with full name, email and a searchable role picker. Each option has a
short permission description; search matches role names only. Selection collapses to the existing summary/Change
pattern. New users require an explicit role selection. There is no password section
on creation; a short footer note explains the setup link shown after creation.
Name/email labels stand alone; intros and save instructions do not repeat the
controls. Helper text explains roles, activation and consequential account changes.
Existing users have matching rows for password reset, enable/disable and deletion
inside Account access. Action labels have no ellipses; confirmation dialogs still
explain consequences before applying them. Cancel/Save close the full edit form,
below account-access controls, with spacing between the buttons. Destructive
dialogs give Cancel initial focus.

Your account shows the current role as a neutral chip beside its title. Change
password is a secondary footer button alongside Cancel/Save, without its own row
or explanatory heading. Theme selection lives in the Profile section on this tab;
the mock sidebar and mobile menu no longer include a theme button. The choice
applies immediately within the mock tab, independently of saving the profile.

Table styling keeps the SLI/SLO alignment, equal 16 px side padding (10 px at
intermediate widths), 24 px first-line alignment and selectable clickable rows.
The name stays left-aligned and other columns centred. Approved for this sparse
management table: let native table sizing share spare width between columns,
instead of assigning it all to Name under ADR-0032. Separate Name/Email columns
keep desktop rows to one line. This changes the mock only; it does not amend the
operational-table contract beyond this settings-specific exception, recorded in
[ADR-0035](../../../adr/0035-settings-and-local-user-administration.md). Role chips reuse the status chip's size,
typography and pill shape, with a neutral outline and no status mark or semantic
colour. Status retains the existing shape/colour vocabulary. Mobile keeps stacked
rows with both status and role visible.

Name entry remains a single Full name field, preserving the person's chosen order
and name parts. Separate given/family/middle fields and an optional preferred name
remain outside this mock. See [W3C's personal-name guidance](https://www.w3.org/International/questions/qa-personal-names).

Last sign-in means the last successful login, not API-key activity. Relative sample
times are fixed against 2026-11-06 14:00 UTC, with exact timestamps on hover and in
accessible labels. Confirmed unused accounts show “Never signed in”; unavailable
history shows “—”. New users start with no sign-in, including after password setup
until they actually log in. Mobile shows a labelled line below each user's details.

The proposal uses the existing [mock foundation](../mock.css),
[approved proposal styles](../proposals/proposal.css) and shell, following the
[design system](../../../../web/DESIGN_SYSTEM.md),
[ADR-0031](../../../adr/0031-list-grammar-and-button-roles.md) and
[ADR-0032](../../../adr/0032-centred-list-tables-and-data-state-vocabulary.md).
The foundation and reference pages are unchanged. This is HTML like the retained
mocks; production implementation must use the shared React controls and demonstrate
any additions in the catalogue.

The existing local-user contracts in
[SPEC-0004](../../../specs/0004-identity-and-access.md) and
[ADR-0020](../../../adr/0020-session-authorized-management.md) govern activation,
password resets, session authority, role boundaries and last-admin protection.
Creation produces a private, one-time 24-hour setup link. Admin password resets
produce a 15-minute link and immediately revoke sessions/tokens and suspend password
login; a pending reset can be cancelled. No automatic email or old password is shown.
Pending admins do not satisfy last-admin protection. Admin is the display label for
the existing `administrator` role; Viewer and Editor remain deployment-wide.

The maintainer explicitly chose **email address for sign-in** on 2026-11-06.
[ADR-0035](../../../adr/0035-settings-and-local-user-administration.md) and the
SPEC-0004 amendment record this direction and preservation of existing username
access. Production implementation and verification are tracked in
[F1-T20](../../../tasks/F1-T20.md); these HTML files remain the approved reference.

## Journeys to review

| Journey | Starting point and expected result |
| --- | --- |
| Settings | `#settings` opens Your account; switch to Users with the tab. The left navigation keeps Settings selected. |
| Browse users | `#users`; click anywhere in a row to open that user, or use the name link with the keyboard. Selecting text does not navigate. Search name/email, combine status and role, page through 12 fixtures or select 20 rows. Clear filters from a no-results state. |
| Add | Users → Add user → name, email and role (type to filter, then choose an option) → Add user → one-time setup-link dialog. Closing it leaves a pending user; the link cannot be retrieved later. |
| Activation | Use Preview recipient journey from the one-time dialog, or `#activate/pending` → set and confirm a password → account ready → email sign-in preview. Returning to the setup route shows an unavailable link. |
| Edit | Select Sam Rivera → change name, email or role → Save changes → confirmation. Duplicate email and blank name retain the form and identify the error. |
| Disable | Sam → Disable user → Cancel or confirm → Disabled; no profile data is removed. |
| Re-enable | Disabled user → Enable user → confirm → Active, or Pending setup if a password has never been set. Prior sessions and links remain revoked. |
| Delete | User → Delete user → Cancel or confirm → user disappears from the list, with a confirmation. Direct navigation to that removed user shows an unavailable state. |
| Reset password | Active user other than the last local admin → Reset password → confirm → one-time reset-link dialog → recipient sets a password. Pending reset status and cancellation are visible after closing the dialog. |
| Replace setup link | Pending user → Replace setup link → confirm → replacement one-time dialog. No email is sent. |
| Your account | `#account` → update your own full name, choose Light/Dark or use the footer's Change password button. The title carries a role chip; sign-in email is managed by an admin in Users. |
| Permission denied | Review controls → Editor or Viewer → Users URL; no other users or their details are shown. Your account remains available. |
| Last local admin | Review controls → Last admin → Change role → choose Viewer and save; error preserves input. Disable, delete and sign-in-suspending reset are unavailable. A pending admin is insufficient. |
| Empty / failure | Review controls → Empty list or Failed list. Try again recovers the simulated read failure. Empty and no-results states differ. |
| Failed save | Review controls → Fail next save, then submit a form; the error keeps the entered values. Correct/retry deliberately. |
| Expired link | Review controls → Expired link; explain that a new link is required, without exposing account details. |
| Keyboard / mobile | Tab through controls, `/` to search outside inputs, Escape to cancel a dialog, then verify focus returns. Mobile rows keep name/email, status and role visible together without horizontal scrolling. |

Review controls are separate from product screens and clearly label the fixtures.
Routes use fragments so the mocks can also be opened directly from disk.

## Implementation boundaries

- **Delete semantics:** remove access/account from management, preserve historical
  audit attribution. Retention, personal-data handling and API semantics need a
  contract before implementation; this mock is not evidence of hard deletion.
- **Email changes:** admins change the sign-in email; changing it does not silently
  add email delivery or verification. Uniqueness, normalization, account recovery
  and existing-account migration need explicit acceptance criteria.
- **Own profile:** every role may edit their name and change their password;
  email/role changes use admin management. The last active local admin cannot
  suspend their only usable sign-in by issuing a reset.
- **Re-enabling:** restore the prior role; an account that never set a password
  returns to pending setup and needs a replacement link.

## Limits

One-time output, expiry, revocation, deletion and role changes are visual scenarios,
not security enforcement. Password length/confirmation, duplicate email and local
state transitions are simulated; common-password screening and authentication are
not. The example links use the reserved `.example` domain and do not grant access.
No tokens/SSO settings, audit-history screen, bulk user actions or production code
are added in this pass.
