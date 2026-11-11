# Runbook: outgoing email

Email is optional. With no SMTP server configured nothing is ever emailed and every flow behaves as it always did: links that could be mailed are shown in the page for the user to copy. This page covers turning it on, what is sent, how delivery works and how to troubleshoot it.

## What the installation sends
The messages Proreta can send:

| Message | Sent when | Link |
|---|---|---|
| Confirm your email address | A person registers with a password (their own organization or through an invitation link), or asks for it again from the banner | `<PRORETA_PUBLIC_URL>/verify-email#<token>` |
| You are invited to an organization | An admin invites someone and gives their address ([invitations](invitations.md#emailing-the-link)) | `<PRORETA_PUBLIC_URL>/invite#<token>` |

All of them:

- come from the one sender address you configure and go to a single recipient;
- are plain text plus a simple HTML alternative, in English, with **no tracking pixels, no remote images and no tracking links**;
- carry a link built only from `PRORETA_PUBLIC_URL`; a token in a link travels in the URL fragment (`#...`), which browsers never send to a server, so it stays out of access logs and referrers;
- are sent one at a time, never to several recipients, and never contain data of another organization.

Deployments that only need the monitoring features can leave email off.

## Email verification
With email on, a person who registers with a password gets a message with a one-time link. Opening it marks the address as verified (`emailVerified` in the session). Until then the web app shows a banner with a button to send the link again.

- The token has 256 random bits; only its SHA-256 is stored. A link works once, for 24 hours, and only the newest link of an account works (asking again invalidates the older ones). It confirms the address it was issued for; if the account's address changed meanwhile, it does nothing.
- The link page works in any browser, signed in or not: the token is the proof. Unknown, expired, used and replaced tokens all get the same answer, and failed attempts are limited per client address.
- Resending is limited to one email a minute and five a day per user.
- The platform admin's login and a user created by an OAuth sign-in are not asked: the latter is verified by the provider when the account is created. A person who links a provider later is verified the same way.
- **Nothing depends on verification.** An unverified user can do everything a verified one can; making verification a condition of anything (for example creating monitors) is not implemented. The banner is a reminder.
- Audit: `auth.email_verification_requested` when a user asks for the link again, `auth.email_verified` when a link is used. Neither holds the address.
- Without email nothing changes: no banner, no message, and `POST /api/v1/auth/resend-verification` and `/auth/verify-email` answer 404.

An address that was registered before email was turned on stays unverified until its owner asks for the link from the banner.

## Turning it on
Set the server in the Helm values (see the [chart README](../../deploy/helm/proreta/README.md)):

```yaml
email:
  enabled: true
  smtp:
    host: smtp.example.com
    # port: 587              # default of the TLS mode
    # tls: starttls          # starttls (default) | tls | none
    from: "Proreta <proreta@example.com>"
    username: mailer         # empty: no login
secrets:
  smtp:
    existingSecret: proreta-smtp   # key: password
```

Create the Secret first, with the password under the key `password`, or let External Secrets read it (`externalSecrets.enabled: true` and `secrets.smtp.remoteKey`, property `password`):

```sh
kubectl -n proreta create secret generic proreta-smtp --from-literal=password='...'
```

The chart renders the settings as environment variables:

| Variable | Process | Meaning |
|---|---|---|
| `PRORETA_SMTP_HOST` | api, jobs | Server name or address, no port. Empty or unset: email is off. |
| `PRORETA_SMTP_PORT` | api, jobs | Default 587 for `starttls`, 465 for `tls`, 25 for `none`. |
| `PRORETA_SMTP_TLS` | api, jobs | `starttls` (default), `tls` (implicit TLS) or `none`. |
| `PRORETA_SMTP_FROM` | api, jobs | Sender, `proreta@example.com` or `Proreta <proreta@example.com>`. Required with a host. |
| `PRORETA_SMTP_USERNAME`, `PRORETA_SMTP_PASSWORD` | jobs only | The login. Both or neither. The password comes from the Secret. |

Rules the backend enforces at start-up (it refuses to start otherwise, naming the variable):

- `starttls` **never downgrades**: a server that does not offer STARTTLS is an error, not a plain-text session. Certificates are verified against the system roots and the host name.
- The login is sent only over TLS. `none` is accepted only for `localhost`, `127.0.0.1` or `::1` (a relay sidecar in the same pod, or local development) and cannot be combined with a login.
- Any `PRORETA_SMTP_*` variable without `PRORETA_SMTP_HOST` is an error, so a half-written configuration does not silently turn email off.

The api reads only the non-secret settings: it queues messages and tells the web app that email is on (`emailEnabled` in `GET /api/v1/auth/providers`). **Only the `jobs` Deployment gets the login**, and it is the only process that connects to the server. After changing the settings, roll out `api` and `jobs`.

### Sender domain
Use a `from` address on a domain whose SPF and DKIM records allow your SMTP provider to send for it, and a DMARC policy you control. Without them messages land in spam or are rejected. Proreta cannot check this for you.

## Check the settings
From inside the cluster, with the settings of the `jobs` Deployment:

```sh
kubectl --context <cluster-context> -n proreta exec deploy/proreta-jobs -- /app/proretactl mail test -to you@example.org
```

`mail test` connects directly to the server with the `PRORETA_SMTP_*` variables of that pod, sends one message and prints the step that failed (`connect`, `tls`, `auth`, `recipient_rejected 550`, ...). It does not touch the database or the queue, and it never prints what the server answered, because a reply can contain the address.

## How delivery works
A message is queued in the same database transaction as the change it announces (for example creating an invitation), in the table `mail_outbox`, and delivered a moment later by `jobs`. So:

- a message exists exactly when its change was committed, and a slow or down mail server never slows a request or loses a message;
- the row holds the recipient and the one-time token **encrypted** (AES-256-GCM with the installation's encryption key, bound to the row). As soon as the message is sent or given up, the encrypted payload is emptied; finished rows are deleted after 14 days, and verification tokens a day after they expire;
- delivery is at least once: a crash between the SMTP acceptance and the database update can send a message twice;
- `jobs` sends at most 4 messages at once and starts at most 4 per second per process. A failed send is retried after 30 s, 2 min, 10 min and 30 min; the fifth failure gives up. A rejected recipient (a 5xx answer to the address or the message) is not retried. A message that has outlived the token it carries is dropped, never sent late;
- `jobs` runs as one replica; if it is stopped, messages wait in the queue and go out when it is back.

If email is switched off while messages are queued, they stay in the queue until they expire.

## Metrics and logs
Metrics (all in the [observability runbook](observability.md)): `proreta_mail_enqueued_total{kind}` (api), `proreta_jobs_mail_total{kind,result}`, `proreta_jobs_mail_duration_seconds`, `proreta_jobs_mail_pending`, `proreta_jobs_mail_claim_errors_total`.

Logs never contain a message body, an address or a token. A failure is logged once per attempt as `mail not sent` with the message id, the kind and an error kind such as `connect`, `tls`, `auth`, `timeout` or `recipient_rejected 550`.

Suggested alerts: `proreta_jobs_mail_pending > 20` for 10 minutes (mail is not leaving), and `increase(proreta_jobs_mail_total{result="failed"}[1h]) > 0`.

## Troubleshooting
| Symptom | Likely cause |
|---|---|
| `jobs` crash-loops with `PRORETA_SMTP_...` in the error | A setting is missing or contradictory (see the rules above). |
| `mail not sent ... error_kind=tls` | The server does not offer STARTTLS on that port, or its certificate does not match the host name. Use the port of the right mode (587 `starttls`, 465 `tls`). |
| `error_kind=auth 535` | Wrong login or password. The message is retried, so fixing the Secret and restarting `jobs` is enough if you are within the retry window. |
| `error_kind=recipient_rejected 550` | The server refused that address. The user must correct it; nothing is retried. |
| `error_kind=rejected 5xx` | The server refused the sender or the content (SPF/DKIM/DMARC, or a relay policy). Check the `from` domain. |
| Messages arrive in spam | Sender domain authentication, see above. |
| `proreta_jobs_mail_pending` keeps growing | `jobs` is not running, the server is down, or the rate limit of your provider is lower than the traffic. |

## Not covered
- Bounce and complaint handling (an SMTP server that accepts and bounces later is invisible to Proreta).
- Per-organization sender addresses and branding, and translations: one sender and English only.
- Email notifications for monitor alerts: alerts still go to Slack channels only.
