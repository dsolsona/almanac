# Contracts and callers

Read [API and CLI](../../../../docs/specs/0010-api-and-cli.md) and relevant protocol
or resource specs when their boundaries change.

- Compare old and new inputs, outputs, nullability, error codes, enums, defaults,
  configuration keys and persisted shapes. A stable function signature can still
  conceal a behavioral contract change.
- Search all consumers of each changed contract, including the HTTP adapters,
  CLI, web client, agent, workers, imports and fixtures that actually use it.
  Inspect handling of newly possible errors, statuses and absent values.
- When a guard changes, enumerate sibling creation and mutation paths. Check
  that an import, bulk operation or background worker cannot bypass the invariant.
- Review OpenAPI and SQL sources alongside generated consumers. Confirm that
  generation is reproducible rather than recommending edits to generated output.
- Distinguish a supported import from an executable definition. Unsupported valid
  definitions must retain explicit inactive reasons under the accepted OpenSLO profile.
- Evaluate existing saved definitions and independently running agent versions
  against the actual compatibility contract; avoid inventing a broader guarantee.

Report the changed contract and a named broken consumer or bypass. Search outside
the diff: an untouched caller can become defective because its dependency changed.
