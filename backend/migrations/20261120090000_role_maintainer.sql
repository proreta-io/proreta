-- +goose Up
SET LOCAL lock_timeout = '5s';

-- The role admin is renamed maintainer. Audit rows keep the old name.
ALTER TABLE memberships DROP CONSTRAINT memberships_role_check;
ALTER TABLE invitations DROP CONSTRAINT invitations_role_check;
ALTER TABLE org_auth_domains DROP CONSTRAINT org_auth_domains_default_role_check;

-- FORCE ROW LEVEL SECURITY applies to the owner too and no policy lets it see these rows.
ALTER TABLE memberships NO FORCE ROW LEVEL SECURITY;
ALTER TABLE invitations NO FORCE ROW LEVEL SECURITY;
ALTER TABLE org_auth_domains NO FORCE ROW LEVEL SECURITY;

UPDATE memberships SET role = 'maintainer' WHERE role = 'admin';
UPDATE invitations SET role = 'maintainer' WHERE role = 'admin';
UPDATE org_auth_domains SET default_role = 'maintainer' WHERE default_role = 'admin';

ALTER TABLE memberships FORCE ROW LEVEL SECURITY;
ALTER TABLE invitations FORCE ROW LEVEL SECURITY;
ALTER TABLE org_auth_domains FORCE ROW LEVEL SECURITY;

ALTER TABLE memberships ADD CONSTRAINT memberships_role_check
    CHECK (role IN ('owner', 'maintainer', 'member', 'viewer'));
ALTER TABLE invitations ADD CONSTRAINT invitations_role_check
    CHECK (role IN ('maintainer', 'member', 'viewer'));
ALTER TABLE org_auth_domains ADD CONSTRAINT org_auth_domains_default_role_check
    CHECK (default_role IN ('maintainer', 'member', 'viewer'));
