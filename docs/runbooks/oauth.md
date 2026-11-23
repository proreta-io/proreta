# Runbook: sign-in with Google and GitHub

This page covers how to set up the providers, how to switch to OAuth-only sign-in, and how to recover access.

## How it works for users
- The login page shows one button per configured provider (**Sign in with Google**, **Sign in with GitHub**). If password sign-in is on, the password form is shown below the buttons.
- A user is found by the provider's stable id (Google `sub`, GitHub numeric id) first. If nobody has that identity yet and an account with the same email exists, the provider sign-in **links the identity to that account**: the provider has proven the address, so its owner gets the account. Organization memberships and data stay, and the email counts as verified. What happens to the password depends on whether the address was verified before this sign-in: if it was, the password stays (both ways to sign in work while password sign-in is on); if it was not (the default while email verification is off), the password is **removed**, together with the account's open password reset and verification links, because whoever registered the address with that password never proved it was theirs. That person can no longer sign in with it. Every other session of the account ends at that moment; the session of this sign-in stays. The link is audited as `auth.identity_linked` with `via: sign_in` and `passwordRemoved` (`true` when the password was removed).
- **What is never linked this way:** an account that already has a provider identity, a disabled account, a platform admin and the break-glass admin. For those Proreta answers "An account with this email already exists" (`/login?error=account_exists`): sign in the usual way and link the provider under **Settings → Account → Sign-in methods** (`/profile`). If that old account is not wanted, the platform admin can delete it ([admin panel](admin-panel.md), "Delete an account"); the address and the provider identity are then free and the next provider sign-in creates a clean new account.
- **Consequence while email verification is off (the default):** someone who registers an address they do not own with a password is locked out once the real owner signs in with Google or GitHub: the link removes the password (see above), so they are locked out; an owner who registered with a password and a typed own address and never verified it loses the password at the first provider sign-in too and signs in with the provider from then on (a reset or a temporary password from the admin panel gives the account a password again). A provider session does not force a password change; only a later password sign-in is asked for a new password.
- A new account is created only through an invitation (the invitation page also offers the provider buttons), through a Google Workspace domain claimed by an organization, or when self-registration is open (`PRORETA_REGISTRATION_ENABLED`). Anyone else gets "There is no Proreta account for this sign-in" (`not_invited`).
- **A link bound to an address** ([invitations](invitations.md#binding-a-link-to-one-address)) is opened only by the provider sign-in whose **verified** email is that address, whatever the case; any other address ends at `/login?error=invitation_email_mismatch` and nothing changes (no membership, the link stays open). This is the hard control: the provider has proven the address. If the address already has an account without a provider identity, the sign-in still links the provider to that account first, as above, and then refuses the join. A link that is not bound accepts any provider address.
- Error codes in `/login?error=<code>` and `/profile?error=<code>` are listed in the spec of `oauthCallback`. The provider's own error text is never shown.

## Redirect URIs
Proreta sends exactly this redirect URI. Register it at the provider character for character: https, no trailing slash, the same host as `PRORETA_PUBLIC_URL` (Helm `config.publicUrl`).

| Provider | Redirect / callback URL |
|---|---|
| Google | `<PRORETA_PUBLIC_URL>/api/v1/auth/oauth/google/callback`, for example `https://proreta.example.com/api/v1/auth/oauth/google/callback` |
| GitHub | `<PRORETA_PUBLIC_URL>/api/v1/auth/oauth/github/callback` |

If you change `PRORETA_PUBLIC_URL`, update both providers before deploying. Otherwise every sign-in fails at the provider with a redirect URI mismatch.

## Creating the Google OAuth client
1. In Google Cloud Console, use (or create) a project owned by the company's Workspace organization.
2. **APIs & Services → OAuth consent screen**: set the user type to **Internal**. Only accounts of your Workspace can then sign in, and no Google verification is needed. Fill in the app name (Proreta) and the support email. Scopes: `openid`, `email`, `profile` (the defaults; Proreta asks for nothing else).
3. **Credentials → Create credentials → OAuth client ID**, application type **Web application**:
   - Authorized JavaScript origins: none needed.
   - Authorized redirect URIs: the Google URL from the table above.
4. Copy the client id and the client secret into the secret (below). Do not paste them into chats, tickets or values files.

Proreta requires `email_verified` on the Google account and reads `hd` (the Workspace domain). Gmail accounts have no `hd`, so they never join through a domain.

## Creating the GitHub OAuth app
1. In GitHub, go to **Settings → Developer settings → OAuth Apps → New OAuth App**. Create it under the company's GitHub organization if you can, so that it does not depend on one person's account.
2. Homepage URL: `PRORETA_PUBLIC_URL`. Authorization callback URL: the GitHub URL from the table above. Leave device flow off.
3. Generate a client secret and copy the client id and the secret into the secret (below).

Proreta asks for the scopes `read:user user:email` and uses only the **primary, verified** email from `/user/emails`. The access token is used for those two calls and then discarded. GitHub users join only through an invitation, by linking, or through open registration; there is no domain or organization mapping.

## The OAuth secret
The client credentials never go into values files. Keys: `google_client_id`, `google_client_secret`, `github_client_id`, `github_client_secret`; only those of the enabled providers are read. Provide them in one of two ways:

**A Secret you create (no External Secrets needed).** Type the values instead of passing them as arguments, so they stay out of your shell history; end with Ctrl-D:
```sh
kubectl --context <cluster-context> -n proreta create secret generic proreta-oauth-clients --from-env-file=/dev/stdin
google_client_id=1234-abc.apps.googleusercontent.com
google_client_secret=...
```
and name it in your values file:
```yaml
secrets:
  oauth:
    existingSecret: proreta-oauth-clients
```

**Your secret store, through the External Secrets Operator** (`externalSecrets.enabled: true`, `secrets.oauth.existingSecret` empty). The chart renders the ExternalSecret `proreta-oauth` that reads the JSON secret `secrets.oauth.remoteKey` (default `proreta/oauth`):

```json
{
  "google_client_id": "1234-abc.apps.googleusercontent.com",
  "google_client_secret": "…",
  "github_client_id": "Iv1.…",
  "github_client_secret": "…"
}
```

Rendering fails when a provider is enabled and neither source is configured. Then enable the providers in your values file and roll out (`helm upgrade`, or with ArgoCD: commit and let it sync):

```yaml
oauth:
  google:
    enabled: true
  github:
    enabled: true
```

The api reads the credentials only at startup. After rotating them (edit the Secret, or update the store and force a refresh of the ExternalSecret, whose `refreshInterval` is `"0"`), restart the api pods: `kubectl --context <cluster-context> -n proreta rollout restart deploy/proreta-api`. The api refuses to start if a provider has only one of the two values.

## Sign-in domains (Google Workspace auto-join)
The operator decides which domains may be claimed at all: `PRORETA_OAUTH_ALLOWED_DOMAINS` (Helm `oauth.allowedDomains`, lowercase). It is empty by default, which turns claiming and auto-join off. Example for the company domain:

```yaml
oauth:
  allowedDomains: [example.com]
```

Then an **owner** of the organization:
1. Opens **Settings → Sign-in domains** and chooses **Sign in with Google again**, using a Google account of that domain. This proves control for 10 minutes. If another Google account is already linked to the owner, they first unlink it under **Sign-in methods** (`/profile`), or they use that account. The proof comes back to `/profile`; the Settings link there leads back to the domains section.
2. Enters the domain and chooses **Claim domain**. A new domain starts with the role **viewer**; the owner can raise it to member or maintainer afterwards.

From then on, a person whose verified Google account has `hd = example.com` joins that organization on their **first** Google sign-in. Existing accounts do not join. A domain belongs to one organization only.

When the first person of an allowed Workspace domain signs in with Google and registration is open, the organization created for them claims the domain automatically (role viewer), so colleagues who sign in later join it instead of creating their own. Password and GitHub sign-ups never claim a domain.

Why there is an allow-list: every employee of a Workspace has the same `hd`, so without it anyone could claim the company domain for an organization of their own. If a domain was claimed by the wrong organization, remove it from `oauth.allowedDomains` and deploy (auto-join stops at once). Then the owner of that organization releases it, or the platform admin deletes that organization. All changes are audited as `organization.auth_domain_changed`.

## Going OAuth-only without losing the admin
1. Configure at least one provider and check that people can sign in with it. Have every user link a provider on `/profile` while they still have their password.
2. Set `config.passwordLoginEnabled: false`. The chart and the api refuse this setting if no provider is configured.
3. The password form disappears for everyone. The platform admin (`PRORETA_ADMIN_LOGIN`) keeps it at **`/login?admin`**: this is the break-glass account, so keep its password somewhere safe (the admin changes it under Profile; the Secret `proreta-admin` only matters for a reset, see the [admin panel runbook](admin-panel.md)).
4. Password registration is refused as well. Invitations then work only through the provider buttons.

## Recovery
- **A user lost access to their provider account** (left the company, GitHub account deleted): OAuth-only users have no password and no self-service recovery. Remove them from the organization and invite them again with a new provider account. If they still have another linked method, they can sign in with it and unlink the old one on `/profile`.
- **A provider is down or misconfigured.** There are two symptoms:
  - Google's discovery document cannot be loaded and there is no cached copy. The sign-in buttons answer "The provider cannot be reached right now" (503 from the start request), and the api logs `oauth start failed`.
  - Otherwise, the code exchange fails: sign-ins return to `/login?error=failed`, the api logs `oauth code exchange failed`, and `proreta_auth_oauth_logins_total{result="failed"}` rises.

  In both cases, sign in as the admin at `/login?admin`. If the problem is configuration (redirect URI, rotated secret), fix it at the provider or in the secret and restart the api. Temporarily setting `config.passwordLoginEnabled: true` again gives password users their form back.
- **"The sign-in took too long or was started in another browser"** (`state_invalid`): the flow must finish in the same browser within 10 minutes. Rotating the encryption key also ends flows that are in progress ([rotation and backup of the key](../install.md#back-up-the-secrets)). Start again.
- **Someone cannot unlink a provider** ("This is your only way to sign in"): link another provider first. When password sign-in is off, a password does not count, except for the admin.
- **Too many attempts** (`rate_limited`): failed callbacks share the login limit per client address (30 failures per 15 minutes per api replica). Wait, or check for a misconfigured client causing repeated failures.

## Audit
`auth.oauth_login_succeeded`, `auth.oauth_login_failed` (with a short reason code, never the provider's text; `invitation_email_mismatch` for a bound link opened with another address), `auth.identity_linked`, `auth.identity_unlinked`, `member.joined` (with `via: auth_domain` for domain joins) and `organization.auth_domain_changed`.
