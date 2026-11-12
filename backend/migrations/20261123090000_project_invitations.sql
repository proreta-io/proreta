-- +goose Up
SET LOCAL lock_timeout = '5s';

-- An invitation may carry a project. For a project invitation role is the person's role in that
-- project (any of the four) and no organization role is granted; for an organization invitation
-- (project_id NULL) it stays the organization role.
ALTER TABLE invitations ADD COLUMN project_id uuid;
ALTER TABLE invitations ADD CONSTRAINT invitations_project_fk
    FOREIGN KEY (tenant_id, project_id) REFERENCES projects (tenant_id, id);
ALTER TABLE invitations DROP CONSTRAINT invitations_role_check;
ALTER TABLE invitations ADD CONSTRAINT invitations_role_check CHECK (
    (project_id IS NULL AND role IN ('maintainer', 'member', 'viewer'))
    OR (project_id IS NOT NULL AND role IN ('owner', 'maintainer', 'member', 'viewer')));
CREATE INDEX invitations_project_idx ON invitations (tenant_id, project_id, created_at DESC)
    WHERE project_id IS NOT NULL;

-- auth_invitation_lookup reads the project of an invitation before the tenant is known.
CREATE POLICY owner_invitation_project_lookup ON projects FOR SELECT TO proreta_owner USING (true);

-- The lookup also returns the project (id and name); an invitation whose project was deleted
-- returns no row, like any closed invitation. The return type changes, so the function is replaced.
DROP FUNCTION auth_invitation_lookup(bytea);
-- +goose StatementBegin
CREATE FUNCTION auth_invitation_lookup(p_token_hash bytea)
RETURNS TABLE (
    invitation_id  uuid,
    tenant_id      uuid,
    tenant_name    text,
    role           text,
    expires_at     timestamptz,
    project_id     uuid,
    project_name   text
)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
    SELECT i.id, i.tenant_id, t.name, i.role, i.expires_at, i.project_id, p.name
    FROM invitations AS i
    JOIN tenants AS t ON t.id = i.tenant_id
    LEFT JOIN projects AS p ON p.tenant_id = i.tenant_id AND p.id = i.project_id
    WHERE i.token_hash = p_token_hash
      AND length(p_token_hash) = 32
      AND i.accepted_at IS NULL
      AND i.revoked_at IS NULL
      AND i.expires_at > now()
      AND t.status = 'active'
      AND t.deleted_at IS NULL
      AND (i.project_id IS NULL OR p.deleted_at IS NULL)
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION auth_invitation_lookup(bytea) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION auth_invitation_lookup(bytea) TO proreta_api;
