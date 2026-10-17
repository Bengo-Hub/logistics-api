-- Vehicle plates are unique per tenant only (vehicle_tenant_id_license_plate stays). The global
-- vehicles_license_plate_key came from a field-level Unique() and blocked a second tenant from
-- registering a plate another tenant already had. cmd/migrate runs the same statement under the
-- advisory lock, because online auto-migrate never drops indexes.
-- Drop index "vehicles_license_plate_key" from table: "vehicles"
DROP INDEX IF EXISTS "vehicles_license_plate_key";
