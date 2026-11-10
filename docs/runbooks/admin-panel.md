# Runbook: platform admin panel

The panel is the `/admin` area of the web app and the `/api/v1/admin/*` operations behind it. Only the platform admin can use it: the system admin configured by `PRORETA_ADMIN_LOGIN` (default `admin`, credentials in the `proreta-admin` Secret). Every other user gets 403 from the API and the not-found page in the web app.

## Who is the platform admin
- `users.is_platform_admin` is set by the api at every start for the configured login and cleared for every other user. There is no API or UI to grant it.
- Changing `PRORETA_ADMIN_LOGIN` moves the flag to the new login at the next rollout; the previous admin is disabled and loses the flag.
- To check: `SELECT email, status FROM users WHERE is_platform_admin;` as `proreta_owner` (one row expected).

## What the panel does
| Action | Where | Effect | Audit row (in the target organization) |
|---|---|---|---|
| Overview | `/admin` | Counts: organizations (active, suspended, deleted), active users, monitors, heartbeats, organizations per plan | none |
| List and search organizations | `/admin/organizations` | Search by name, slug, organization id or the email of a member; 25 per page | none |
| Change plan | organization details, plan selector | The organization's limits change at once (see "Plans and existing usage") | `admin.organization.plan_changed` with `from`, `to` |
| Suspend / unsuspend | organization details | See "Suspension" | `admin.organization.suspended` / `admin.organization.unsuspended` |
| Delete | organization details | Soft delete, see "Deletion" | `admin.organization.deleted` |
| Reset a password | organization details, users table | New temporary password, all sessions of the user deleted | `admin.user.password_reset` with the number of sessions deleted |

Guard rails (enforced in the database, not only in the UI): the `default` organization cannot be suspended or deleted, the admin cannot suspend or delete an organization it is a member of, a deleted organization cannot be changed, and the system admin's password cannot be reset in the panel. `proreta_admin_actions_total{action}` counts the changes ([observability](observability.md)).

## Recover a customer's password
Until outgoing email exists this is the only recovery path.
1. Verify the request through a channel you trust (the customer's known contact, not only an email that claims to be them). Registration proves nothing about who owns an address unless the user confirmed it with the email link (the users table does not show that yet).
2. Open `/admin/organizations`, search for the customer's email, open the organization.
3. In the users table choose **Reset password** for that user and confirm.
4. Copy the temporary password from the panel. It is shown once: the server keeps only its hash, and hiding the panel or leaving the page clears it from the page (the web app keeps no other copy). If it is lost, reset again.
5. Hand it over through a trusted channel, never in a ticket or a shared chat. Tell the user that Proreta will ask for a new password at the next sign-in.
6. The user's open sessions were deleted, and a login that was checking the old password at that moment is refused too; they log in with their email and the temporary password. A temporary password only opens the screen "Set a new password": until the user has chosen their own (different from the temporary one), every other request is refused with `password-change-required`. Until then the temporary password stays valid; reset again if it was lost.

## The system admin's password
The first password of the system admin comes from the Secret, so Proreta asks for a new one at the first sign-in (the same screen as for a temporary password). Afterwards the system admin changes its own password under **Profile → Change password** like any user (current password, new password; 12-128 characters). The change signs the admin out everywhere else, keeps the current browser signed in with a new session, and is audited as `auth.password_changed`.

The panel does not reset it. The hash in the Secret `proreta-admin` (`PRORETA_ADMIN_PASSWORD_HASH`) is applied at every api start with one rule: it replaces the stored password only while the admin never changed it in the app, or when it is a different hash from the one the api saw at its previous start. So:
- A restart or an upgrade with the same Secret never overwrites a password chosen in the app.
- **Initial password:** the first sign-in asks for a new password, and after that the Secret `proreta-admin-initial` is obsolete (its password no longer works) and can be deleted at any time as hygiene (`kubectl -n proreta delete secret proreta-admin-initial`). Nothing reads it; it only holds the first password in plain text.
- **Reset when the password is lost:** put a new hash into the Secret, then restart the api, which applies it, forgets the chosen password and deletes the admin's sessions. With the generated Secrets: delete `proreta-admin` and `proreta-admin-initial`, run the same `helm upgrade` (a new one-time password is generated), `kubectl -n proreta rollout restart deploy/proreta-api`, sign in with the new one-time password and set a new one when asked. With your own secret store or Secret: write a new hash (`proretactl hashpw`) to it and restart the api; the admin is asked to set a new password at the next sign-in, whoever chose the hash. A restart or an upgrade with an unchanged Secret never asks again, and an installation that was running before this behaviour existed is not asked.

## Suspend an organization
Use it for abuse, non-payment or a customer's request to freeze an account.
- **Logins:** a user of the organization who enters the right password gets "account suspended" (403); a wrong password still gets the generic error.
- **Sessions:** existing sessions stop working on the next request (they are kept; unsuspending revives those that have not expired).
- **Checks and alerts:** the worker claims none of its monitors, its heartbeats are not evaluated and pings are answered 204 but not recorded, and its Slack deliveries are not sent. Deliveries that were already queued wait; see the unsuspend note below. Open incidents stay open.
- **Data:** nothing is deleted; monitors, results, incidents, Slack settings and users stay. Retention keeps working as for everybody.
- Unsuspend with the same button. Checks resume within about a minute. Slack deliveries that waited more than 15 minutes are not sent: the unsuspend marks them `failed` with `last_error = 'tenant_suspended'` and the audit row `admin.organization.unsuspended` records how many (`deliveriesExpired`); newer ones are sent at once. Pings sent during the suspension were not recorded, so a heartbeat whose deadline passed meanwhile goes down at the next evaluation (within 30 s) and alerts, unless a ping arrives first; warn the customer. The incident resolves with the next ping.

## Delete an organization
- Soft delete: `deleted_at` is set and the status becomes `suspended`, with every effect of a suspension. Its pending Slack deliveries are marked `failed` (`last_error = 'tenant_deleted'`) by the next delivery claim and are never sent, even after a restore. The organization disappears from the default list (tick "Show deleted" to see it) and can no longer be changed in the panel.
- Nothing is erased. Its users still exist and keep their email: they cannot register again with it, and a login with the right password says "account suspended". To give a deleted customer access again, restore the organization with SQL as `proreta_owner` (`UPDATE tenants SET deleted_at = NULL, status = 'active', updated_at = now() WHERE id = ...`) and record why in your incident notes.

## Plans and existing usage
- A plan change applies at once to new creations only. Existing monitors, heartbeats and the Slack channel are never deleted or paused by a downgrade.
- When usage is above the new limit, the customer keeps everything, can edit, pause and delete, and gets "plan limit reached" (409) when creating more; `GET /api/v1/usage` (and the web app) shows usage above the limit. Tell the customer which monitors to delete if they should get under the limit.
- Per-organization overrides in `tenants.limits` (the organization's row) still win over the plan; the panel does not edit them.
- A plan change and a monitor creation of the same organization are serialised by the organization row lock, so a create cannot slip past a downgrade.

## Audit
Every change writes one `audit_log` row into the target organization with the admin's user id, the client IP and the before/after values; never a password or a hash. There is no viewer in the panel yet; read it as `proreta_owner`:

```sql
SELECT created_at, action, entity_type, entity_id, metadata, host(ip)
FROM audit_log WHERE action LIKE 'admin.%' ORDER BY created_at DESC LIMIT 50;
```

`audit_log` forces RLS for its owner too: run the query in a transaction after `SELECT set_config('app.tenant_id', '<organization id>', true);` for one organization, or as the database server admin login for all of them.

### Naming rules
Audit rows are permanent, so the vocabulary is fixed:
- **`action`** is `<area>.<event>`: lowercase, one dot, the event in past tense with words joined by `_` (`member.role_changed`, `auth.login_failed`, `organization.auth_domain_changed`). Actions of the platform admin have three parts, `admin.<entity>.<event>` (`admin.organization.suspended`, `admin.user.password_reset`), and are also the label values of `proreta_admin_actions_total`. The areas are `auth`, `organization`, `member`, `invitation`, `project`, `monitor`, `channel`, `status_page` and `admin`.
- **`entity_type`** is the singular snake_case noun of the row `entity_id` points at: `organization` (never `tenant`), `user`, `session`, `invitation`, `auth_domain`, `project`, `monitor`, `notification_channel`, `status_page`.
- **`metadata`** keys are camelCase and use the organization vocabulary (`organizationId`, `fromOrganizationId`, `invitationId`, `expiresAt`, `defaultRole`, `newAdminUserId`, `sessionsRevoked`, `deliveriesExpired`, `monitorsDetached`). Values that are codes are lowercase snake_case (`via: auth_domain`, `reason: organization_suspended`). Metadata never holds a secret, a hash, a URL or an email.
- `audit_log.tenant_id` is the column name of the owning organization; it is a column, not a vocabulary item, and keeps the SQL name.
