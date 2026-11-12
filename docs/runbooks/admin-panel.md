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
| Reset a password | organization details, Members table | New temporary password, all sessions of the user deleted. Independent of email: always available | `admin.user.password_reset` with the number of sessions deleted |
| List and search users | `/admin/users` | Every user of every organization, newest first; search by email or name, filter by status (active or disabled); 25 per page | none |
| User details | `/admin/users/<id>` | The user's organizations with role, joined date and organization status, and the number of open sessions. Each organization links to its details | none |
| Disable a user | user details | The user cannot sign in (password, OAuth or invitation) and all of the user's sessions in every organization are deleted at once. Memberships and data stay. Disabling a user who is already disabled does nothing | `admin.user.disabled` (`sessionsRevoked`) |
| Enable a user | user details | The user can sign in again; sessions deleted by the disable stay deleted. Enabling a user who is already active does nothing | `admin.user.enabled` |
| Sign a user out everywhere | user details | Deletes all of the user's sessions in every organization; the account keeps working. Shows how many open sessions were ended | `admin.user.sessions_revoked` (`sessionsRevoked`), only when at least one open session was ended |
| Add a person to an organization | organization details, Members | An existing account is added by email, or a member gets another role (owner, maintainer, member or viewer); a member can be removed, which ends their sessions in that organization. See "Add a person to an organization" | `admin.member.added` (`role`), `admin.member.role_changed` (`from`, `to`), `admin.member.removed` (`role`) |
| Invite someone | organization details, Invitations | A single-use link for a person without an account, shown once, with the same optional email as the organization's own invitations; open invitations can be revoked | `admin.invitation.created` (`role`, `emailed`), `admin.invitation.revoked` (`role`) |
| Email settings | `/admin/email` | The SMTP connection, a test message and three switches, see [outgoing email](email.md). Platform-wide, not per organization | `admin.email_settings.updated` (`changed`: the names of the changed settings, or `removed`, or `switches` with the three switch values; `switchesDisabled`) and `admin.email_test.sent` (`toDomain`), written to the admin's own organization |
| Registration switch | `/admin/registration` | Opens or closes self-service registration (new organizations by password or OAuth sign-up) without a redeploy. A choice stored here wins over `PRORETA_REGISTRATION_ENABLED` (`config.registrationEnabled`); "Follow the installation's configuration" removes the choice. Applies at once on the replica that served it and within about 5 seconds on the others. Invitations, claimed Workspace domains and existing users are not affected | `admin.registration_settings.updated` (`from`, `to`: `enabled`, `disabled` or `environment`), written to the admin's own organization; nothing is written when the value does not change |
| Create a project | organization details, Projects | Adds a project with a name and a slug derived from it (or given through the API). The organization's project limit applies (50) and slugs are unique within it | `admin.project.created` (`slug`) |
| Rename a project | organization details, Projects | Changes the name and/or the slug. Nothing is written when nothing changes | `admin.project.updated` (`changed`: the names of the changed fields) |
| Delete a project | organization details, Projects | Only an empty project: one with monitors or notification channels is refused (`project-not-empty`), and the Default project cannot be deleted (`default-project`). The project's list of people is removed | `admin.project.deleted` (`slug`, `membersRemoved`) |
| Project people | organization details, Projects, People | Adds a member of the organization to a project, makes a member read-only there (override `viewer`) or removes the entry. The person must already be a member of the organization; an override never raises the organization role. Owners and admins see every project and need no entry | `admin.project_member.added` (`userId`, `role`), `admin.project_member.updated` (`userId`, `from`, `to`), `admin.project_member.removed` (`userId`); `role`, `from` and `to` are `viewer`, `member` or `organization` (no override) |

Guard rails (enforced in the database, not only in the UI): the `default` organization cannot be suspended or deleted, the admin cannot suspend or delete an organization it is a member of, a deleted organization cannot be changed, and the system admin's password cannot be reset in the panel. For members: the last active owner of an organization cannot be demoted or removed (409 `last-owner`), the system admin's membership of `default` cannot be changed or removed and the system admin cannot be added to another organization (409 `system-admin`), and adding a member respects the plan's member limit (409 `plan-limit-reached`). Project changes use the same rules as the organization's own owners (caps, slugs, empty-project delete) and are audited only as the `admin.*` actions above, in the organization concerned, with the admin as actor; a deleted organization's projects can be read but not changed. `proreta_admin_actions_total{action}` counts the changes ([observability](observability.md)).

## Add a person to an organization
Use it when nobody in an organization can add people any more (the only owner left, an owner is unreachable) or when a customer asks you to.
1. Open `/admin/organizations`, search for the organization and open it. **Members** lists everyone with their email, role and status.
2. **Someone who already has an account:** under **Add an existing user** type part of the email or the name, pick the account from the list, choose the role and select **Add member**. If nothing matches the panel says so and nothing is written; use an invitation instead. A person who is already a member gets the new role (the same role again changes nothing and writes no audit row). A disabled account can be added, but it cannot sign in.
3. **Someone without an account:** under **Invitations** choose **Invite someone**, the role (`maintainer`, `member` or `viewer`; an owner is made by adding the account or promoting the person after they join) and, if outgoing email is on, optionally an address to send the link to. The link is shown once and never stored; hand it over as described in [members and invitations](invitations.md).
4. To change a role use the selector in the member's row; to remove a person use **Remove**. Making someone an owner, or changing an owner, asks for confirmation. A role change applies on the member's next request; a removal signs the person out of that organization at once and leaves their account and other organizations alone.

Limits and refusals: the plan's member limit counts members plus open invitations and the panel never overrides it (raise the plan first, or revoke invitations nobody will use). A deleted organization cannot be changed, though its members and invitations can still be read. The changes are made with the rules the organization's own owners have, so the last owner and the system admin are protected the same way. The audit rows are written in the organization with your user id and the client IP; never an email, a link or a token.


## Recover a customer's password
On an installation without [outgoing email](email.md) (or with "Allow password reset" off) this is the only recovery path; with it on, users can also use **Forgot your password?** on the sign-in page, and this panel remains for the cases that need a person (an account without a mailbox, a suspected takeover).
1. Verify the request through a channel you trust (the customer's known contact, not only an email that claims to be them). Registration proves nothing about who owns an address unless the user confirmed it with the email link (the users table does not show that yet).
2. Open `/admin/organizations`, search for the customer's email, open the organization.
3. In the Members table choose **Reset password** for that user and confirm.
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
- **`action`** is `<area>.<event>`: lowercase, one dot, the event in past tense with words joined by `_` (`member.role_changed`, `auth.login_failed`, `organization.auth_domain_changed`). Actions of the platform admin have three parts, `admin.<entity>.<event>` (`admin.organization.suspended`, `admin.user.password_reset`, `admin.email_settings.updated`, `admin.email_test.sent`), and are also the label values of `proreta_admin_actions_total`. The areas are `auth`, `organization`, `member`, `invitation`, `project`, `monitor`, `channel`, `status_page` and `admin`.
- **`entity_type`** is the singular snake_case noun of the row `entity_id` points at: `organization` (never `tenant`), `user`, `session`, `invitation`, `auth_domain`, `project`, `monitor`, `notification_channel`, `status_page`.
- **`metadata`** keys are camelCase and use the organization vocabulary (`organizationId`, `fromOrganizationId`, `invitationId`, `expiresAt`, `defaultRole`, `newAdminUserId`, `sessionsRevoked`, `deliveriesExpired`, `monitorsDetached`). Values that are codes are lowercase snake_case (`via: auth_domain`, `reason: organization_suspended`). Metadata never holds a secret, a hash, a URL or an email (the email test records only the recipient's domain).
- `audit_log.tenant_id` is the column name of the owning organization; it is a column, not a vocabulary item, and keeps the SQL name.
