-- +goose Up
SET LOCAL lock_timeout = '5s';

-- A page with a project belongs to it and may show only its monitors; NULL is an
-- organization-level page. Existing pages stay organization-level.
ALTER TABLE status_pages
    ADD COLUMN project_id uuid,
    ADD CONSTRAINT status_pages_project_fkey FOREIGN KEY (tenant_id, project_id) REFERENCES projects (tenant_id, id);
CREATE INDEX status_pages_project_idx ON status_pages (tenant_id, project_id) WHERE deleted_at IS NULL AND project_id IS NOT NULL;
