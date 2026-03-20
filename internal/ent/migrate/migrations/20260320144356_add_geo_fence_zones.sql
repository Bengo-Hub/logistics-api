-- Create "geo_fences" table
CREATE TABLE "geo_fences" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "name" character varying NOT NULL, "zone_type" character varying NOT NULL DEFAULT 'delivery', "status" character varying NOT NULL DEFAULT 'active', "boundary" jsonb NOT NULL, "color" character varying NULL DEFAULT '#3b82f6', "metadata" jsonb NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "geofence_status" to table: "geo_fences"
CREATE INDEX "geofence_status" ON "geo_fences" ("status");
-- Create index "geofence_tenant_id" to table: "geo_fences"
CREATE INDEX "geofence_tenant_id" ON "geo_fences" ("tenant_id");
-- Create index "geofence_tenant_id_name" to table: "geo_fences"
CREATE UNIQUE INDEX "geofence_tenant_id_name" ON "geo_fences" ("tenant_id", "name");
