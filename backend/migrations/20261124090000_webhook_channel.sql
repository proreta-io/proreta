-- +goose Up
SET LOCAL lock_timeout = '5s';

-- A second channel type: a generic webhook that receives the message of a template. Its URL is
-- sealed in config_encrypted like the Slack webhook URL; the template is plain text (it holds no
-- secret) and is validated by the application, the checks keep the shape.
ALTER TABLE notification_channels DROP CONSTRAINT notification_channels_type_check;
ALTER TABLE notification_channels ADD CONSTRAINT notification_channels_type_check
    CHECK (type IN ('slack_webhook', 'webhook'));
ALTER TABLE notification_channels ADD COLUMN content_type text
    CONSTRAINT notification_channels_content_type_check CHECK (content_type IN ('json', 'text'));
ALTER TABLE notification_channels ADD COLUMN body_template text
    CONSTRAINT notification_channels_body_template_check CHECK (char_length(body_template) <= 8192);
ALTER TABLE notification_channels ADD CONSTRAINT notification_channels_webhook_shape CHECK (
    (type = 'webhook' AND content_type IS NOT NULL AND body_template IS NOT NULL AND mention = '{}'::text[])
    OR (type = 'slack_webhook' AND content_type IS NULL AND body_template IS NULL));

-- jobs fills {{monitor.type}}, {{project.name}} and {{organization.name}} when it renders a
-- webhook body. The worker has no access to projects, so the three names come through a function
-- scoped to the transaction's tenant.
-- +goose StatementBegin
CREATE FUNCTION webhook_delivery_context(p_monitor_id uuid)
RETURNS TABLE (monitor_type text, project_name text, organization_name text)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
    SELECT m.type, p.name, t.name
    FROM monitors AS m
    JOIN projects AS p ON p.tenant_id = m.tenant_id AND p.id = m.project_id
    JOIN tenants AS t ON t.id = m.tenant_id
    WHERE m.tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid
      AND m.id = p_monitor_id
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION webhook_delivery_context(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION webhook_delivery_context(uuid) TO proreta_worker;
