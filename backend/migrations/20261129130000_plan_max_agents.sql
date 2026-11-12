-- +goose Up
SET LOCAL lock_timeout = '5s';

-- max_agents: how many Specula (not revoked) an organization may register. plans.Resolve fails
-- on a plan without the key, so every plan gets it; tenants.limits overrides stay optional.
UPDATE plans SET limits = limits || '{"max_agents": 1}'  WHERE key = 'free';
UPDATE plans SET limits = limits || '{"max_agents": 20}' WHERE key = 'internal';
UPDATE plans SET limits = limits || '{"max_agents": 2147483647}' WHERE key = 'community';
