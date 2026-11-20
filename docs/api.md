# API compatibility

The API contract is [api/openapi.yaml](../api/openapi.yaml). From version 1.0 the operations under `/api/v1` are frozen: a change must not break a client written against 1.0.

## What counts as breaking
`oasdiff breaking` in CI decides, with its default rules, against the base branch of the pull request. Typical cases: removing or renaming a path, operation, parameter, field or enum value; making an optional request field required; narrowing a type, format or limit so that a valid request becomes invalid; adding a required request field; changing a status code or the problem `type` of an error.

Not breaking: a new operation, a new optional request parameter or field, a new response field, a new enum value in a response, a larger `maxItems` on a response list, a new problem `type` (clients must ignore unknown fields and tolerate unknown enum values, see the Stability section of the contract). `oasdiff` reports the new response enum value and the larger response `maxItems` as errors by default, so [api/oasdiff-severity-levels.txt](../api/oasdiff-severity-levels.txt) lowers exactly these two rules (`response-property-enum-value-added`, `response-property-max-items-increased`) to warnings. The same additions in a *request* (a new value the server must accept) are unaffected; removing an enum value is still breaking.

Errors stay `application/problem+json` (RFC 9457); clients match on the `type` URN, never on `title` or `detail`.

The Specula API ([api/specula.yaml](../api/specula.yaml), the wire contract between the gateway and executors in customer networks) follows the same rules: executors of the previous minor release must keep working against a newer gateway. It is checked against the Go shapes by a test, not yet by `oasdiff` in CI.

## Making a breaking change
It needs a written decision (an ADR in the maintainers' records) and an entry in [api/oasdiff-err-ignore.txt](../api/oasdiff-err-ignore.txt) with a comment naming it. The file is empty at 1.0. Prefer a new operation or a new version of the path over changing an existing one.

## Deprecation
An operation or field is first marked `deprecated: true` with the release and a replacement in its description, and stays working for at least one minor release (and at least 90 days) before it can be removed, which then is a breaking change that needs the decision above. The release notes of the version that deprecates it say so.

## Experimental operations
An operation marked `x-experimental: true` is not frozen: it may change in any release without an ADR. CI leaves these operations out of the comparison (`scripts/strip-experimental.py`); an operation that is stable on the base branch and experimental on the head is reported as removed, so a stable operation cannot be demoted by accident. Graduating an operation (removing the marker) needs no decision; it is frozen from the next release.

Experimental today:
- every operation under `/api/v1/admin/` (the platform admin API behind the bundled admin panel; a test requires the marker on all of them);
- the status page domain operations: `listStatusPageDomains`, `addStatusPageDomain`, `setPrimaryStatusPageDomain`, `removeStatusPageDomain`, `verifyStatusPageDomain` (`/api/v1/status-pages/{statusPageId}/domains`).

Adding or removing the marker changes this list; keep it in sync.

## Versioning
`info.version` in the contract is the API version and follows semver: a new operation or field raises the minor version, a breaking change the major one. The release tag and the chart version are separate.
