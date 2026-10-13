-- Logistics roles become platform reference data (one row per role code, no tenant_id); user role
-- assignments stay tenant-scoped. Applied at startup by cmd/migrate globalizeRoles() under the
-- migration advisory lock (this service applies schema by live diff, so this file records the
-- change). Backups: *_bk_global_20261013.
CREATE TABLE IF NOT EXISTS logistics_roles_bk_global_20261013 AS SELECT * FROM logistics_roles;
CREATE TABLE IF NOT EXISTS role_permissions_bk_global_20261013 AS SELECT * FROM role_permissions;
CREATE TABLE IF NOT EXISTS user_role_assignments_bk_global_20261013 AS SELECT * FROM user_role_assignments;
CREATE TEMP TABLE role_map ON COMMIT DROP AS
  SELECT r.id AS old_id, c.id AS new_id
  FROM logistics_roles r
  JOIN (SELECT DISTINCT ON (role_code) role_code, id FROM logistics_roles ORDER BY role_code, created_at, id) c USING (role_code)
  WHERE r.id <> c.id;
DELETE FROM user_role_assignments a USING role_map m
  WHERE a.role_id = m.old_id AND EXISTS (SELECT 1 FROM user_role_assignments b
    WHERE b.tenant_id = a.tenant_id AND b.user_id = a.user_id AND b.role_id = m.new_id);
UPDATE user_role_assignments a SET role_id = m.new_id FROM role_map m WHERE a.role_id = m.old_id;
DELETE FROM role_permissions p USING role_map m WHERE p.role_id = m.old_id;
DELETE FROM logistics_roles r USING role_map m WHERE r.id = m.old_id;
DROP INDEX IF EXISTS logisticsrole_tenant_id_role_code;
DROP INDEX IF EXISTS logisticsrole_tenant_id;
ALTER TABLE logistics_roles DROP COLUMN tenant_id;
-- The old LogisticsRole.user_assignments edge.To added this never-set FK column; the edge now
-- reuses role_id.
ALTER TABLE user_role_assignments DROP COLUMN IF EXISTS logistics_role_user_assignments;
CREATE UNIQUE INDEX IF NOT EXISTS logisticsrole_role_code ON logistics_roles (role_code);
