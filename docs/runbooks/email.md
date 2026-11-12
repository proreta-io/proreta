# Runbook: outgoing email

Email is optional and **off by default**. Until the platform admin configures an SMTP server, tests it and turns a feature on, nothing is ever emailed and every flow behaves as it always did: links that could be mailed are shown in the page for the user to copy. All email settings live in the admin panel (**Admin → Email**), not in the Helm values or the environment, so they change without a redeploy. This page covers setting it up, what is sent, how delivery works and how to troubleshoot it.

## What the installation sends
The messages Proreta can send:

| Message | Sent when | Link |
|---|---|---|
| Confirm your email address | A person registers with a password (their own organization or through an invitation link) and "Send email verification" is on, or asks for it again from the banner | `<PRORETA_PUBLIC_URL>/verify-email#<token>` |
| Reset your password | Someone asks for it on the sign-in page, and "Allow password reset" is on ("Forgot your password?") | `<PRORETA_PUBLIC_URL>/reset-password#<token>` |
| You are invited to an organization | A maintainer invites someone, gives their address and keeps "Send by email" checked ([invitations](invitations.md#emailing-the-link)) | `<PRORETA_PUBLIC_URL>/invite#<token>` |

All of them:

- come from the one sender address configured in the panel and go to a single recipient;
- are plain text plus a simple HTML alternative, in English, with **no tracking pixels, no remote images and no tracking links**;
- carry a link built only from `PRORETA_PUBLIC_URL`; a token in a link travels in the URL fragment (`#...`), which browsers never send to a server, so it stays out of access logs and referrers;
- are sent one at a time, never to several recipients, and never contain data of another organization.

Deployments that only need the monitoring features can leave email off.

## Email verification
With "Send email verification" on, a person who registers with a password gets a message with a one-time link. Opening it marks the address as verified (`emailVerified` in the session). Until then the web app shows a banner with a button to send the link again.

- The token has 256 random bits; only its SHA-256 is stored. A link works once, for 24 hours, and only the newest link of an account works (asking again invalidates the older ones). It confirms the address it was issued for; if the account's address changed meanwhile, it does nothing.
- The link page works in any browser, signed in or not: the token is the proof. Unknown, expired, used and replaced tokens all get the same answer, and failed attempts are limited per client address.
- Resending is limited to one email a minute and five a day per user.
- The platform admin's login and a user created by an OAuth sign-in are not asked: the latter is verified by the provider when the account is created. A person who links a provider later is verified the same way.
- **Nothing depends on verification.** An unverified user can do everything a verified one can; making verification a condition of anything (for example creating monitors) is not implemented. The banner is a reminder.
- Audit: `auth.email_verification_requested` when a user asks for the link again, `auth.email_verified` when a link is used. Neither holds the address.
- With the switch off nothing changes from the user's side: no banner, no message, and `POST /api/v1/auth/resend-verification` and `/auth/verify-email` answer 404 (links sent earlier stop working while it is off and work again, until they expire, if it is turned back on; turning it off deletes nothing).

An address that was registered before verification was turned on stays unverified until its owner asks for the link from the banner.

## Forgotten passwords
With "Allow password reset" on (and the password form on, `config.passwordLoginEnabled`), the sign-in page offers **Forgot your password?**. The user enters their address and always sees the same answer ("if that address has an account, a link is on its way"); the API answers 202 whether or not the address is registered, so the form cannot be used to find out who has an account. The platform admin's **temporary password** action in the admin panel is independent of email: it stays as it is and is always available, and it is the way for users on an installation without email.

- The link works once, for **one hour**, and only the newest link of an account works. It carries a 256-bit token in the URL fragment; only its SHA-256 is stored. It is tied to the address the account had when it was issued.
- Only an active account with a password gets a link. Accounts that sign in only with Google or GitHub, disabled accounts and the platform admin's login get nothing (and the same answer). A user who has only a provider cannot add a password this way.
- Limits (all answered the same, so they reveal nothing): 10 requests and attempts per client address per 15 minutes (429 with `Retry-After`, per api replica); beyond that, 3 requests per address per hour and 3 links per account per hour are answered 202 and send nothing.
- Setting the new password uses the same bounded hashing queue as sign-in, so a flood is answered 429 instead of exhausting memory. The link is checked before the hash is computed.
- **Completing a reset ends every session of the account in every organization**, closes the other open reset links, and marks the address verified (the link went to that mailbox). It does not sign the user in.
- Audit: `auth.password_reset_requested` (a link was issued) and `auth.password_reset_completed` (`sessionsRevoked`); neither holds the address or the token. Requests that send nothing are not audited; they are counted in the logs as rate-limited.
- The answers are "constant-ish": a request for a registered address does a few more database writes than one for an unknown address (milliseconds), and the mail itself leaves later. Someone with precise timing and many requests per client address could in principle tell them apart; the per-address and per-client limits keep that impractical.
- **When the switch is off** (or email is not enabled, or the password form is off), `POST /api/v1/auth/forgot-password` **and** `POST /api/v1/auth/reset-password` answer 404, nothing is queued, the sign-in page shows no link and the reset page says the feature is not available. Reset links issued **before** the switch was turned off do not work while it is off: turning the switch off deletes nothing, so those links work again, until their own one-hour expiry, if it is turned back on. Turning it off is therefore also the emergency stop for an abused flow.

## Setting it up
Sign in as the platform admin and open **Admin → Email**. Nothing is needed in the Helm values: there are no `email.*` values and no `PRORETA_SMTP_*` variables.

1. **Connection.** Enter host, port, TLS mode, sender (`proreta@example.com` or `Proreta <proreta@example.com>`) and, if the server needs one, username and password. Saving does not send anything.
2. **Send a test email.** The panel queues a real message to an address you give (default: your own login when it is an address). `jobs` sends it, once and without retries, through the same code that sends every other message, and records the result; the panel polls and shows the time and outcome (see [Troubleshooting](#troubleshooting)). Only a successful test of the **current** connection settings counts: changing the host, port, TLS mode, sender, username or password forgets it and turns all switches off.
3. **Switches.** The three switches are off by default and cannot be turned on until a test succeeded:
   - *Send invitation links by email*: the invite dialog's "Send by email" box is pre-checked. (Once the server is tested a maintainer can always email an invitation by checking the box; the switch only sets the default.)
   - *Send email verification*: a verification message at registration.
   - *Allow password reset*: the forgot-password flow.

   Turning a switch off is always allowed and deletes nothing.

The whole chain is also available as an API for automation (`/api/v1/admin/email`, `/features`, `/test`, marked experimental like the rest of the admin API).

### What the settings mean
| Setting | Meaning |
|---|---|
| Host | Server name or IP address, without scheme or port. |
| Port | Default 587 for `starttls`, 465 for `tls`, 25 for `none`. |
| TLS | `starttls` (default; upgrades the connection and **never downgrades**: a server without STARTTLS is an error), `tls` (TLS from the first byte) or `none` (plain, accepted only for `localhost`, `127.0.0.1` or `::1`, for example a relay sidecar). Certificates are verified against the system roots and the host name. |
| Sender | The From address of every message. |
| Username, password | The login, sent only over TLS. Empty username: no login (a relay that trusts the network). |

### How the password is kept
- The password is sealed (AES-256-GCM) with the installation's encryption key (`proreta-crypto`), with the id of the key stored next to it, exactly like stored Slack webhook URLs. Losing the key loses it: enter it again.
- It is **write-only**: no API or screen returns it, only "a password is set". To change it, enter a new one; leaving the field empty keeps the stored one.
- Changing the host, port or username while keeping the stored password is refused: type the password again. A stolen admin session therefore cannot make the installation log in to a server of the attacker's choosing with the stored password.
- Only `jobs` reads the sealed value (through a database function only its role can run) and decrypts it. The api never sees it.
- Audit: `admin.email_settings.updated` (the names of the changed settings, never values) and `admin.email_test.sent` (only the recipient's domain). Both are written to the admin's own organization.
- Removing the server (the panel's "Remove server") erases the password and turns every switch off.

### Which servers are allowed
The platform admin picks the host, so the SMTP client is not subject to the blocked ranges that protect monitors and webhooks: **a relay on a private network (10.x, 172.16-31.x, 192.168.x, unique-local IPv6) works**, and so does a service name inside the cluster. What is always refused, when saving an IP address and again at connect time for whatever a name resolves to: link-local addresses (169.254.0.0/16 and fe80::/10, which includes the cloud metadata service), the other metadata addresses (AWS IPv6, Azure, Alibaba, Oracle), unspecified (0.0.0.0/8, ::), multicast and broadcast. Every network step has a timeout (10 s to connect, 30 s for the whole message).

### Several replicas
The api keeps the switches for up to 5 seconds. After a change the replica that served it applies it at once and the others within about 5 seconds; `jobs` reads the connection for every message. After turning the password reset off, a request that reaches another replica in that window may still be accepted.

### Sender domain
Use a sender address on a domain whose SPF and DKIM records allow your SMTP provider to send for it, and a DMARC policy you control. Without them messages land in spam or are rejected. Proreta cannot check this for you.

## How delivery works
A message is queued in the same database transaction as the change it announces (for example creating an invitation), in the table `mail_outbox`, and delivered a moment later by `jobs`. So:

- a message exists exactly when its change was committed, and a slow or down mail server never slows a request or loses a message;
- the row holds the recipient and the one-time token **encrypted** (AES-256-GCM with the installation's encryption key, bound to the row). As soon as the message is sent or given up, the encrypted payload is emptied; finished rows are deleted after 14 days, and verification tokens a day after they expire;
- delivery is at least once: a crash between the SMTP acceptance and the database update can send a message twice;
- `jobs` sends at most 4 messages at once and starts at most 4 per second per process. A failed send is retried after 30 s, 2 min, 10 min and 30 min; the fifth failure gives up. A rejected recipient (a 5xx answer to the address or the message) is not retried. A message that has outlived the token it carries is dropped, never sent late;
- `jobs` runs as one replica; if it is stopped, messages wait in the queue and go out when it is back.

While no server is configured `jobs` claims nothing: messages wait until the server is set up or they expire. Settings are read again for every message, so a corrected password applies to the next attempt. The test message is the exception: it is sent once, never retried, and expires after two minutes.

## Metrics and logs
Metrics (all in the [observability runbook](observability.md)): `proreta_mail_enqueued_total{kind}` (api), `proreta_jobs_mail_total{kind,result}`, `proreta_jobs_mail_duration_seconds`, `proreta_jobs_mail_pending`, `proreta_jobs_mail_claim_errors_total`.

Logs never contain a message body, an address or a token. A failure is logged once per attempt as `mail not sent` with the message id, the kind and an error kind such as `connect`, `tls`, `auth`, `timeout` or `recipient_rejected 550`.

Suggested alerts: `proreta_jobs_mail_pending > 20` for 10 minutes (mail is not leaving), and `increase(proreta_jobs_mail_total{result="failed"}[1h]) > 0`.

## Troubleshooting
The panel shows the last test with a category (never the server's own text, which can echo addresses) and the SMTP code when there is one:

| Result | Likely cause |
|---|---|
| `connect` | The connection broke or the server spoke something that is not SMTP. Check host and port. |
| `refused` | Nothing listens on that host and port (or a firewall rejects it). |
| `dns` | The host name does not resolve from the `jobs` pod. |
| `blocked_address` | The name resolves to a link-local, metadata or unspecified address, which is never used. |
| `tls` | The server does not offer STARTTLS on that port, or its certificate does not match the host name or is not trusted. Use the port of the right mode (587 `starttls`, 465 `tls`). |
| `auth` (535) | Wrong username or password. |
| `timeout` | The server did not answer in time. |
| `recipient_rejected` (550) | The server refused the test address. |
| `rejected` (5xx) | The server refused the sender or the content (SPF/DKIM/DMARC, or a relay policy). Check the sender domain. |
| `not_delivered` | `jobs` did not pick the test up within three minutes: it is not running or cannot read the settings. |

Other symptoms:

| Symptom | Likely cause |
|---|---|
| A switch cannot be turned on | No successful test of the current settings (any change of a connection setting forgets it). |
| Messages are not arriving but the test passed | Check `proreta_jobs_mail_pending` and `proreta_jobs_mail_total{result="failed"}`, and the `mail not sent` log lines (kind and error category, no address). |
| Messages arrive in spam | Sender domain authentication, see above. |
| `proreta_jobs_mail_pending` keeps growing | `jobs` is not running, the server is down, or your provider's rate limit is lower than the traffic. |
| `jobs` logs `reading the email settings failed` | The database is unreachable, or the stored password cannot be decrypted (the encryption key changed): enter the password again. |

## Not covered
- Bounce and complaint handling (an SMTP server that accepts and bounces later is invisible to Proreta).
- Per-organization sender addresses and branding, and translations: one sender and English only.
- Email notifications for monitor alerts: alerts still go to Slack channels only.
