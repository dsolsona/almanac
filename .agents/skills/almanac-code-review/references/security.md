# Security and credentials

Read [identity and access](../../../../docs/specs/0004-identity-and-access.md),
[secret encryption](../../../../docs/adr/0017-recoverable-secret-encryption.md),
and the applicable accepted authorization ADRs.

## OWASP coverage for Almanac

Use the [OWASP Top 10:2025](https://owasp.org/Top10/2025/) to identify relevant
attack paths in the change. The table guides investigation; findings require
reachable defects and evidence, and a code review does not establish OWASP compliance.

| Risk | Almanac review focus |
| --- | --- |
| A01 Broken Access Control | Role checks, direct object access (IDOR), path traversal, SSRF and agent ownership on every protected operation. |
| A02 Security Misconfiguration | TLS verification, CORS, secure cookie attributes, origin checks, exposed diagnostics, security headers and container privileges. |
| A03 Software Supply Chain Failures | Changed Go/npm dependencies, lockfiles, image pins, generators and CI actions; verify relevant advisories before alleging a vulnerable dependency. |
| A04 Cryptographic Failures | Secret encryption, key loading, ciphertext binding, protected-file permissions and credential exposure in storage or transport. |
| A05 Injection | Stored, reflected and DOM-based XSS; SQL and command injection; unsafe template evaluation. |
| A06 Insecure Design | Trust boundaries, unauthorized credential issuance, unsafe replay and unbounded requests that can exhaust collection or backend resources. |
| A07 Authentication Failures | Login throttling, initial-password restrictions, session expiry/logout and agent credential rotation or revocation. |
| A08 Software or Data Integrity Failures | Untrusted imports/configuration, provenance checks, unauthorized observation changes and executable data or artifact substitution. |
| A09 Security Logging and Alerting Failures | Missing security-relevant audit events and errors that conceal rejected authority; credentials leaking through logs, traces or artifacts. |
| A10 Mishandling of Exceptional Conditions | Auth or validation failing open, partial privileged writes and timeout/error paths leaving usable authority or corrupted state. |

## Browser attacks and rendering

- Trace service/SLI/SLO names, annotations, provider responses and URL state into
  rendering. Check cross-site scripting (XSS) through `dangerouslySetInnerHTML`,
  direct DOM writes and ECharts HTML tooltip formatters. React's escaped text output
  does not protect strings passed to a separate HTML renderer.
- Verify output encoding for the actual HTML, attribute, script or URL context,
  and sanitization where HTML is deliberately accepted. Inspect unsafe URL schemes
  and open redirects. Normal React text interpolation is not itself an XSS defect.
  Use the [XSS prevention guidance](https://cheatsheetseries.owasp.org/cheatsheets/Cross_Site_Scripting_Prevention_Cheat_Sheet.html)
  when the sink's protection is unclear.
- Check cross-site request forgery (CSRF), session-bound tokens and origin validation
  on mutations; GET must not mutate state. Inspect permissive credentialed CORS,
  cookie attributes and clickjacking protections when those boundaries change.
  CSP and HttpOnly cookies can limit impact but do not repair an injection sink.

## API, agent and operational boundaries

- Follow input into SQL, process arguments, shell construction and file paths.
  Verify parameterization, argument handling and path containment at the actual sink.
- Check server-side request forgery (SSRF) in datasource queries, diagnostics and
  notification URLs, including redirects and credentials sent to another host.
  Compare permitted schemes, destinations and credential forwarding with the current
  connectivity policy; a private address alone does not establish a violation.
- Trace the actor, current authority and operation through registration, rotation,
  revocation and ownership proofs. A heartbeat or valid identifier does not authorize
  an observation submission or release of datasource credentials.
- Trace secrets through serialization, HTTP headers, errors, logs, traces, command
  arguments and artifacts. Inspect output handling and existing tests with synthetic
  values, including nested serialized strings; a redaction marker does not prove
  the secret was removed. Do not execute payloads during review.
- Check key/ciphertext binding and credential handling on restore against the current policy.
  Inspect unsafe deserialization and oversized or deeply nested inputs for execution,
  validation bypass or resource exhaustion. Ordinary validated JSON decoding is not
  automatically unsafe.

A finding identifies the actor or credential source, transformations, sensitive
operation/output, and who can exploit or receive it. Missing evidence is not proof
of an exploit. Use disposable payloads and isolated fixtures; keep real secrets and
private data out of findings. A single-tenant deployment still requires user authorization.
