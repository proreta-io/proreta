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

A person can also have no organization role. They belong to the organization only through projects: they see the organization in the switcher, count against the plan's member limit, see only the projects they have a role in and nothing else at the organization level (plan, status page, sign-in domains). The member list shows them only the people they share a project with (everyone with an organization role, who is in every project, and everyone with a role in one of their projects), by name and never by email address, so the rest of the organization stays private to them. They can leave the organization. To make someone project-only, change their role to "Projects only" under **Members**. To give anyone a project role, open **Projects** under the person on **Members**: it lists every project with that person's role there, and a project role is changed right in that list ("Inherited" or "No access" takes it away). The **People** panel of a project on the **Projects** page does the same for one project at a time. Someone with no organization role and no project role sees nothing and can only switch or leave.

The API refuses anything above a role with 403 `urn:proreta:problem:forbidden` and answers 404 for a project the caller has no role in; the web hides those controls and says who can do it. A role change applies on the member's next request. Nobody grants a role above their own, and a maintainer never changes an owner. Every organization keeps at least one active owner: the last one cannot step down, lose the role, be removed or leave (409 `urn:proreta:problem:last-owner`). Ownership transfer is "make someone owner, then step down". A project has no last-owner rule: organization owners always control every project. Organization-wide notification channels are managed by organization owners and maintainers only.

## Where the settings are
The top bar shows **Monitors**, **Status pages** and **Channels** to people who may read them in at least one project, and **Settings** to everybody. **Settings** lists what the signed-in person may manage and links to the screen that does it: under **Organization** the people and invitations, the projects, the channels and status pages shared by the whole organization, the sign-in domains and the plan and usage; under **Projects** each project the person manages, with its people and invitations, channels and status pages. An entry appears only for people whose role holds the permission, and an address they may not use shows "Page not found". **Settings → Roles and permissions** (`/help/roles`) lists what each role may do and marks the signed-in person's roles.

## Inviting someone
An invitation goes either into the **organization** or into one **project**. The organization kind below is for organization owners and maintainers; for the project kind see [Inviting into a project](#inviting-into-a-project).

1. A maintainer or owner opens **Members**, chooses **Invite someone**, picks the role and optionally a note for themselves (for example the person's name). When the platform admin has set up and tested [outgoing email](email.md) the dialog also offers an optional **Email** field and a **Send by email** checkbox, pre-checked when the admin turned on "Send invitation links by email".
2. The link (`<PRORETA_PUBLIC_URL>/invite#<token>`) is shown **once**. Proreta stores only its SHA-256. Copy it and hand it over over a channel you trust (a direct message, not a shared channel or a ticket). Without an address in the dialog, or on an installation without email, nothing is sent.
3. The person opens it. Without an account they register on that page (this works even when `PRORETA_REGISTRATION_ENABLED=false`, because the invitation is the authorization); they get no organization of their own. With an account they sign in and choose **Join**; the session moves to the new organization. Their other organizations stay; the organization switcher in the account menu (the square with the initials in the top bar) moves between them.

**Someone who already has an account** (for example a user removed from every organization, who can no longer sign in: a login without any membership gets the generic "wrong login or password") uses the same link: on the invitation page they choose **Already have an account? Join with it** and enter that account's email and password. The password is checked like a login; a wrong one is answered like a failed login and the link stays usable. Failed attempts count per address, per invitation and per login.

### Inviting into a project
Owners and maintainers of a project (and organization owners and maintainers, for every project) open **Projects → People** of the project and choose **Invite someone** in the **Invitations** panel below the people. They pick the role in the project (`viewer`, `member`, `maintainer` or, for a project or organization owner, `owner`; nobody grants a role above their own there), an optional note and, when email is on, an optional address, exactly as for the organization. The link is shown once and works once for 7 days.

- **What accepting does.** The invitation grants a role in that project and **no organization role**. A new person registers on the invitation page and joins the organization with an empty organization role (they appear as "Projects only"); someone with an account from another organization joins the same way; someone who already belongs to the organization keeps their organization role and only gets the project role. If they already hold an equal or higher role in the project, through the organization or a direct role, nothing changes and the invitation is used up. A role is never lowered by an invitation.
- **The page and the email** name the project and the role; the page also says the role applies in that project only.
- **Who sees which invitation.** Organization owners and maintainers see every invitation. A project owner or maintainer sees and revokes only the invitations into their own projects (not the organization ones, not another project's, which answer 404). An invitation to a role above the revoker's own role in the project is refused (403). `GET /api/v1/invitations?projectId=` narrows the list to one project.
- **Limits.** An open project invitation counts toward the plan's member limit like any other, whether or not the person already belongs. Deleting the project makes its open invitations unusable (they read as revoked) and frees their places; accepting one whose project is gone is the same "not valid" answer as for a revoked link.
- **The system admin** (`PRORETA_ADMIN_LOGIN`) cannot accept a project invitation either (409).
- A person with no organization role cannot create organization invitations; the API answers 403.

### Emailing the link
With an address the same link is also sent by email, from the sender configured in Admin → Email. The message names the organization (and the project for a project invitation), the role and the inviter's display name (the inviter's login is not shared) and nothing else; the link is in the text and HTML parts, with the token in the fragment. You still get the link in the dialog, so you can hand it over yourself if the email does not arrive.

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

A user who is already a member gets 409 `already-member`; they switch to the organization from the account menu instead. The system admin (`PRORETA_ADMIN_LOGIN`) cannot join other organizations: use a separate normal account, which can be granted platform admin on the user page.

## Rolling back an image
Rolling an image back across a migration is not supported: api, worker and jobs refuse to start when the database schema is newer than their binary. Roll forward instead. Between releases with the same migrations an image rollback is fine.

## Organization switcher
The switcher is in the account menu: the square with the person's initials at the right of the top bar, which also holds the theme, **Profile** and **Log out**. `GET /api/v1/auth/organizations` lists the user's organizations; switching (`POST /api/v1/auth/switch-organization`) checks the membership and that the organization is active, then replaces the session (new cookie and CSRF token; the old session is deleted). A suspended organization is listed but cannot be selected. Login still lands in the organization `auth_login_tenant` picks (`default` first, then active before suspended, then the oldest membership).

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
Actions: `member.invited`, `invitation.revoked`, `member.joined` (all three carry `projectId` for a project invitation; `member.joined` also says `existingMember` and whether a role was `granted`), `member.role_changed` (`from`, `to`), `member.removed` (`role`, `self`), `organization.switched` (`fromOrganizationId`), `organization.deleted`. `member.invited` carries `emailed: true` when an email was queued. No token, link or address is ever written to the audit log or the logs. Runtime roles cannot read `audit_log`; query it as `proreta_owner`:
```sql
SELECT created_at, action, actor_user_id, entity_id, metadata FROM audit_log
WHERE tenant_id = '<organization id>' AND (action LIKE 'member.%' OR action LIKE 'invitation.%' OR action LIKE 'organization.%')
ORDER BY created_at DESC LIMIT 50;
```
