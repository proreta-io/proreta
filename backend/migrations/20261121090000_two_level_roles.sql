-- +goose Up
SET LOCAL lock_timeout = '5s';

-- A person may belong to an organization only through projects: no organization role (NULL).
-- The CHECK on memberships.role stays; a NULL passes it.
ALTER TABLE memberships ALTER COLUMN role DROP NOT NULL;

-- project_members.role was an optional override that could only lower the organization role. It is
-- now the direct role in the project, one of the four roles, and the effective role is the higher
-- of the two. Existing rows keep the effective role they had: a NULL override becomes member, and
-- a person whose organization role is viewer keeps viewer (otherwise the new rule would raise it).
ALTER TABLE project_members DROP CONSTRAINT project_members_role_check;

-- FORCE ROW LEVEL SECURITY applies to the owner too and no policy lets it see these rows.
ALTER TABLE memberships NO FORCE ROW LEVEL SECURITY;
ALTER TABLE project_members NO FORCE ROW LEVEL SECURITY;

UPDATE project_members AS pm
SET role = CASE WHEN m.role = 'viewer' THEN 'viewer' ELSE coalesce(pm.role, 'member') END
FROM memberships AS m
WHERE m.tenant_id = pm.tenant_id AND m.user_id = pm.user_id;

ALTER TABLE memberships FORCE ROW LEVEL SECURITY;
ALTER TABLE project_members FORCE ROW LEVEL SECURITY;

ALTER TABLE project_members ALTER COLUMN role SET NOT NULL;
ALTER TABLE project_members ADD CONSTRAINT project_members_role_check
    CHECK (role IN ('owner', 'maintainer', 'member', 'viewer'));
