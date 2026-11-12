# Runbook: members, roles and invitations

This page is the practical side of organizations, roles and invitations for the people who run Proreta and help its users.

## Roles
The same four roles exist on the organization and on a project. A role on the organization applies to every project of it; a direct role on a project applies to that project only; the role someone has in a project is the higher of the two, so a project role can raise an organization role there and never lowers it. [roles.md](../roles.md) lists every permission of every role.

| Role | On the organization | In a project |
|---|---|---|
| `owner` | everything a maintainer may, plus grant and revoke `owner`, delete projects and the organization, manage sign-in domains | delete the project, grant and revoke `owner` in it, and everything a maintainer may |
| `maintainer` | invite people, change and remove maintainers, members and viewers (never owners), create projects, manage organization-level status pages, organization-wide notification channels, and the status pages, channels and people of every project | manage the people (up to `maintainer`), the settings, the notification channels and the status pages of the project |
| `member` | create, edit, pause and delete monitors and heartbeats in every project | the same in the project |
| `viewer` | read every project's monitors, incidents and channels, the member names and the status page settings; organization-wide channels show their name only | read the project |

A person can also have no organization role. They belong to the organization only through projects: they see the organization in the switcher, count against the plan's member limit, see only the projects they have a role in and nothing at the organization level (members, invitations, plan, organization-level status pages, sign-in domains), apart from the names of organization-wide channels, which they may select for their monitors. They can leave the organization. To make someone project-only, change their role to "No organization role (projects only)" under **Members**; to give them a project role use the **People** panel of the project. Someone with no organization role and no project role sees nothing and can only switch or leave.

The API refuses anything above a role with 403 `urn:proreta:problem:forbidden` and answers 404 for a project the caller has no role in; the web hides those controls and says who can do it. A role change applies on the member's next request. Nobody grants a role above their own, and a maintainer never changes an owner. Every organization keeps at least one active owner: the last one cannot step down, lose the role, be removed or leave (409 `urn:proreta:problem:last-owner`). Ownership transfer is "make someone owner, then step down". A project has no last-owner rule: organization owners always control every project. Organization-wide notification channels are managed by organization owners and maintainers only.

## Inviting someone
1. A maintainer or owner opens **Members**, chooses **Invite someone**, picks the role and optionally a note for themselves (for example the person's name). When the platform admin has set up and tested [outgoing email](email.md) the dialog also offers an optional **Email** field and a **Send by email** checkbox, pre-checked when the admin turned on "Send invitation links by email".
2. The link (`<PRORETA_PUBLIC_URL>/invite#<token>`) is shown **once**. Proreta stores only its SHA-256. Copy it and hand it over over a channel you trust (a direct message, not a shared channel or a ticket). Without an address in the dialog, or on an installation without email, nothing is sent.
3. The person opens it. Without an account they register on that page (this works even when `PRORETA_REGISTRATION_ENABLED=false`, because the invitation is the authorization); they get no organization of their own. With an account they sign in and choose **Join**; the session moves to the new organization. Their other organizations stay; the switcher in the top bar moves between them.

**Someone who already has an account** (for example a user removed from every organization, who can no longer sign in: a login without any membership gets the generic "wrong login or password") uses the same link: on the invitation page they choose **Already have an account? Join with it** and enter that account's email and password. The password is checked like a login; a wrong one is answered like a failed login and the link stays usable. Failed attempts count per address, per invitation and per login.

### Emailing the link
With an address the same link is also sent by email, from the sender configured in Admin → Email. The message names the organization, the role and the inviter's display name (the inviter's login is not shared) and nothing else; the link is in the text and HTML parts, with the token in the fragment. You still get the link in the dialog, so you can hand it over yourself if the email does not arrive.

- **The invitation is not bound to the address.** The address is not stored on the invitation (when the note is empty and the address is short, it becomes the note so the list says who was invited), and whoever opens the link can register or join with any email address. An emailed link is as much a bearer secret as a copied one: it is personal, but not tied to a mailbox. The message says so and asks the person not to forward it.
- **The registered address is still unverified.** A person who joins through the link registers an address of their own choosing. If the installation sends email they are sent the usual verification link for it ([email verification](email.md#email-verification)); until they open it the address shows as unverified. Members and owners see the address as typed.
- The email is queued in the same transaction as the invitation and delivered a moment later, so a plan-limit refusal sends nothing and a revoked invitation's email, if not yet read, carries a link that no longer works. "Queued" means accepted by Proreta, not delivered to the mailbox: check the [delivery metrics](observability.md) if people say it never came.
- **To send it again, revoke the invitation and invite again** (the token cannot be recovered, since only its hash is stored). Each new email counts against the limits below.
- Limits: 30 invitation emails per organization per hour and 3 per recipient address per day (per api replica); beyond them the API answers 429 with `Retry-After`. Open invitations also count toward the plan's member limit.
- Without a tested mail server the dialog shows neither field nor checkbox, and the API refuses a request that asks for an email with 422, rather than pretend it sent one. The API's `sendEmail` flag (optional, default: send when an `email` is given) lets a client keep the address out without sending.

A link works once and for 7 days. Open invitations count toward the plan's member limit (`max_members`: `free` 3, `internal` 50) until they are used, revoked or expire, so revoke the ones nobody will use.

### When nobody in the organization can invite
The platform admin can invite people, add an existing account, change roles and remove members from **Admin → Organizations → (the organization)**, without being a member; see "Add a person to an organization" in [the admin panel runbook](admin-panel.md). Those changes are audited as `admin.invitation.created`, `admin.invitation.revoked`, `admin.member.added`, `admin.member.role_changed` and `admin.member.removed`, in place of the `member.*` names above.

## A link was sent to the wrong person or leaked
Revoke it at once: **Members → Invitations → Revoke**. If it was already used, the list shows who joined (`acceptedBy`); remove that member (**Remove**), which ends their sessions in the organization immediately. Both are audited (`invitation.revoked`, `member.removed`).

The token travels in the URL fragment, which browsers never send to a server, so it is not in the gateway's or the api's access logs. It can still be in the browser history and in whatever chat it was pasted into.

## "This invitation link is not valid"
The answer is the same for an unknown, expired, used or revoked link, and for an organization that is suspended or deleted. Ask a maintainer of the organization for a new link. Too many failed attempts from one address are answered 429 for up to 15 minutes (20 failures per 15 minutes per api replica).

A user who is already a member gets 409 `already-member`; they switch to the organization from the top bar instead. The system admin (`PRORETA_ADMIN_LOGIN`) cannot join other organizations: use a separate normal account, which can be granted platform admin on the user page.

## Rolling back an image
Rolling an image back across a migration is not supported: api, worker and jobs refuse to start when the database schema is newer than their binary. Roll forward instead. Between releases with the same migrations an image rollback is fine.

## Organization switcher
`GET /api/v1/auth/organizations` lists the user's organizations; switching (`POST /api/v1/auth/switch-organization`) checks the membership and that the organization is active, then replaces the session (new cookie and CSRF token; the old session is deleted). A suspended organization is listed but cannot be selected. Login still lands in the organization `auth_login_tenant` picks (`default` first, then active before suspended, then the oldest membership).

## Leaving and deleting
- **Leave organization** (any role) ends the membership and the session. The last owner and the system admin cannot leave.
- **Delete organization** (owner only, never `default`) is the same soft delete the platform admin does: monitors stop, deliveries are dropped, every member loses access. Restoring needs SQL as `proreta_owner` (see [admin-panel.md](admin-panel.md)).

## Recovering an organization without an owner
The API prevents it, but data fixed by hand could leave one without an active owner (for example the only owner's user was disabled). As `proreta_owner`, inside a transaction with the organization set:
```sql
BEGIN;
SELECT set_config('app.tenant_id', '<organization id>', true);
SELECT m.user_id, u.email, m.role, u.status FROM memberships m JOIN users u ON u.id = m.user_id WHERE m.tenant_id = '<organization id>';
UPDATE memberships SET role = 'owner' WHERE tenant_id = '<organization id>' AND user_id = '<user id>';
COMMIT;
```
Write down who asked and why; this bypasses the audit log.

## Audit
Actions: `member.invited`, `invitation.revoked`, `member.joined`, `member.role_changed` (`from`, `to`), `member.removed` (`role`, `self`), `organization.switched` (`fromOrganizationId`), `organization.deleted`. `member.invited` carries `emailed: true` when an email was queued. No token, link or address is ever written to the audit log or the logs. Runtime roles cannot read `audit_log`; query it as `proreta_owner`:
```sql
SELECT created_at, action, actor_user_id, entity_id, metadata FROM audit_log
WHERE tenant_id = '<organization id>' AND (action LIKE 'member.%' OR action LIKE 'invitation.%' OR action LIKE 'organization.%')
ORDER BY created_at DESC LIMIT 50;
```
