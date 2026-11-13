-- Custom domains of status pages. The module owns status_page_domains; the DNS check results are
-- applied by the definer status_page_domain_record_check, not here.

-- name: InsertDomain :exec
INSERT INTO status_page_domains (id, tenant_id, page_id, host, token)
VALUES (@id, @tenant_id, @page_id, @host, @token);

-- name: ListPageDomains :many
-- The unreleased domains of a page, the primary one first.
SELECT id, host, state, is_primary, token, verified_at, last_checked_at, last_error,
       (disabled_at IS NOT NULL)::boolean AS disabled, created_at
FROM status_page_domains
WHERE tenant_id = @tenant_id AND page_id = @page_id AND released_at IS NULL
ORDER BY is_primary DESC, created_at, id;

-- name: LockPageDomain :one
SELECT id, host, state, is_primary, token, verified_at, last_checked_at, last_error,
       (disabled_at IS NOT NULL)::boolean AS disabled, created_at
FROM status_page_domains
WHERE tenant_id = @tenant_id AND page_id = @page_id AND id = @id AND released_at IS NULL
FOR UPDATE;

-- name: CountPageDomains :one
SELECT count(*)::integer FROM status_page_domains
WHERE tenant_id = @tenant_id AND page_id = @page_id AND released_at IS NULL;

-- name: CountDomainsAddedSince :one
-- Domains the organization added since a time, released ones included, and when the oldest of
-- them was added.
SELECT count(*)::integer AS n, coalesce(min(created_at), 'epoch'::timestamptz)::timestamptz AS oldest
FROM status_page_domains
WHERE tenant_id = @tenant_id AND created_at > @since;

-- name: ClearPagePrimary :exec
UPDATE status_page_domains SET is_primary = false, updated_at = now()
WHERE tenant_id = @tenant_id AND page_id = @page_id AND is_primary AND released_at IS NULL;

-- name: SetDomainPrimary :exec
UPDATE status_page_domains SET is_primary = true, updated_at = now()
WHERE tenant_id = @tenant_id AND id = @id AND released_at IS NULL;

-- name: PromotePagePrimary :exec
-- When the page has no primary domain, the oldest one that serves becomes it.
UPDATE status_page_domains AS u SET is_primary = true, updated_at = now()
WHERE u.tenant_id = @tenant_id AND u.id = (
        SELECT d.id FROM status_page_domains AS d
        WHERE d.tenant_id = @tenant_id AND d.page_id = @page_id AND d.released_at IS NULL
          AND d.state IN ('verified', 'active') AND d.disabled_at IS NULL
        ORDER BY d.created_at, d.id LIMIT 1)
  AND NOT EXISTS (
        SELECT 1 FROM status_page_domains AS p
        WHERE p.tenant_id = @tenant_id AND p.page_id = @page_id AND p.is_primary AND p.released_at IS NULL);

-- name: ReleaseDomain :exec
UPDATE status_page_domains SET released_at = now(), is_primary = false, updated_at = now()
WHERE tenant_id = @tenant_id AND id = @id AND released_at IS NULL;
