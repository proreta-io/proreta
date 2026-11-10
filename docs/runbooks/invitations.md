# Runbook: members, roles and invitations

This page is the practical side of organizations, roles and invitations for the people who run Proreta and help its users.

## Roles
| Role | May |
|---|---|
| `owner` | everything an admin may, plus grant and revoke `owner` and delete the organization |
| `admin` | invite people, change and remove admins, members and viewers (never owners), notification channels, every monitor |
| `member` | create, edit, pause and delete monitors and heartbeats |
| `viewer` | read monitors, incidents, channels, members and the plan |

The API refuses anything above a role with 403 `urn:proreta:problem:forbidden`; the web hides those controls and says who can do it. A role change applies on the member's next request. Every organization keeps at least one active owner: the last one cannot step down, be removed or leave (409 `urn:proreta:problem:last-owner`). Ownership transfer is "make someone owner, then step down".

## Inviting someone
1. An admin or owner opens **Members**, chooses **Invite someone**, picks the role and optionally a note for themselves (for example the person's name).
2. The link (`<PRORETA_PUBLIC_URL>/invite#<token>`) is shown **once**. Proreta stores only its SHA-256. Copy it and hand it over over a channel you trust (a direct message, not a shared channel or a ticket). No email is sent.
3. The person opens it. Without an account they register on that page (this works even when `PRORETA_REGISTRATION_ENABLED=false`, because the invitation is the authorization); they get no organization of their own. With an account they sign in and choose **Join**; the session moves to the new organization. Their other organizations stay; the switcher in the top bar moves between them.

**Someone who already has an account** (for example a user removed from every organization, who can no longer sign in: a login without any membership gets the generic "wrong login or password") uses the same link: on the invitation page they choose **Already have an account? Join with it** and enter that account's email and password. The password is checked like a login; a wrong one is answered like a failed login and the link stays usable. Failed attempts count per address, per invitation and per login.

A link works once and for 7 days. Open invitations count toward the plan's member limit (`max_members`: `free` 3, `internal` 50) until they are used, revoked or expire, so revoke the ones nobody will use.

## A link was sent to the wrong person or leaked
Revoke it at once: **Members → Invitations → Revoke**. If it was already used, the list shows who joined (`acceptedBy`); remove that member (**Remove**), which ends their sessions in the organization immediately. Both are audited (`invitation.revoked`, `member.removed`).

The token travels in the URL fragment, which browsers never send to a server, so it is not in the gateway's or the api's access logs. It can still be in the browser history and in whatever chat it was pasted into.

## "This invitation link is not valid"
The answer is the same for an unknown, expired, used or revoked link, and for an organization that is suspended or deleted. Ask an admin of the organization for a new link. Too many failed attempts from one address are answered 429 for up to 15 minutes (20 failures per 15 minutes per api replica).

A user who is already a member gets 409 `already-member`; they switch to the organization from the top bar instead. The platform admin cannot join other organizations: use a separate normal account.

## Rolling back an image
Rolling an image back across a migration is not supported: api, worker and jobs refuse to start when the database schema is newer than their binary. Roll forward instead. Between releases with the same migrations an image rollback is fine.

## Organization switcher
`GET /api/v1/auth/organizations` lists the user's organizations; switching (`POST /api/v1/auth/switch-organization`) checks the membership and that the organization is active, then replaces the session (new cookie and CSRF token; the old session is deleted). A suspended organization is listed but cannot be selected. Login still lands in the organization `auth_login_tenant` picks (`default` first, then active before suspended, then the oldest membership).

## Leaving and deleting
- **Leave organization** (any role) ends the membership and the session. The last owner and the platform admin cannot leave.
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
Actions: `member.invited`, `invitation.revoked`, `member.joined`, `member.role_changed` (`from`, `to`), `member.removed` (`role`, `self`), `organization.switched` (`fromOrganizationId`), `organization.deleted`. No token or link is ever written to the audit log or the logs. Runtime roles cannot read `audit_log`; query it as `proreta_owner`:
```sql
SELECT created_at, action, actor_user_id, entity_id, metadata FROM audit_log
WHERE tenant_id = '<organization id>' AND (action LIKE 'member.%' OR action LIKE 'invitation.%' OR action LIKE 'organization.%')
ORDER BY created_at DESC LIMIT 50;
```
