# Security policy

## Reporting a vulnerability

Please do not open a public issue for a security problem.

Report it privately through GitHub: open the repository's **Security** tab and choose **Report a vulnerability** (GitHub private vulnerability reporting / security advisories). Include what you found, how to reproduce it, the affected version or commit, and the impact you expect.

We will acknowledge the report, keep you informed while we investigate, and credit you in the advisory if you wish.

## Supported versions

Proreta has no stable release yet. Fixes go into the `main` branch and the next release.

## Scope

Of particular interest: tenant isolation (row-level security), authentication and sessions, the SSRF guard of the monitoring checks, secret handling, and the public status pages.
