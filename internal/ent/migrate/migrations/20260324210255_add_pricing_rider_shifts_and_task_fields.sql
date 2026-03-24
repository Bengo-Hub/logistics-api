-- Modify "fleet_members" table
ALTER TABLE "fleet_members" ADD COLUMN "specialization_tags" jsonb NOT NULL, ADD COLUMN "has_cold_storage" boolean NOT NULL DEFAULT false, ADD COLUMN "max_weight_capacity_kg" double precision NULL;
-- Modify "tasks" table
ALTER TABLE "tasks" ADD COLUMN "package_weight_kg" double precision NULL, ADD COLUMN "package_dimensions_cm" jsonb NULL, ADD COLUMN "requires_temperature_control" boolean NOT NULL DEFAULT false, ADD COLUMN "temperature_range" character varying NULL, ADD COLUMN "requires_fragile_handling" boolean NOT NULL DEFAULT false, ADD COLUMN "requires_heavy_duty" boolean NOT NULL DEFAULT false, ADD COLUMN "carrier_id" character varying NULL;
-- Create "pricing_rules" table
CREATE TABLE "pricing_rules" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "name" character varying NOT NULL, "rule_type" character varying NOT NULL DEFAULT 'distance', "base_fee" double precision NOT NULL DEFAULT 0, "per_km_rate" double precision NULL, "per_kg_rate" double precision NULL, "surge_multiplier" double precision NULL, "time_windows" jsonb NULL, "distance_tiers" jsonb NULL, "priority" bigint NOT NULL DEFAULT 0, "is_active" boolean NOT NULL DEFAULT true, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "pricingrule_tenant_id_is_active" to table: "pricing_rules"
CREATE INDEX "pricingrule_tenant_id_is_active" ON "pricing_rules" ("tenant_id", "is_active");
-- Create index "pricingrule_tenant_id_rule_type" to table: "pricing_rules"
CREATE INDEX "pricingrule_tenant_id_rule_type" ON "pricing_rules" ("tenant_id", "rule_type");
-- Create "rider_shifts" table
CREATE TABLE "rider_shifts" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "fleet_member_id" uuid NOT NULL, "shift_start" timestamptz NOT NULL, "shift_end" timestamptz NOT NULL, "status" character varying NOT NULL DEFAULT 'scheduled', "zone_ids" jsonb NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "ridershift_status" to table: "rider_shifts"
CREATE INDEX "ridershift_status" ON "rider_shifts" ("status");
-- Create index "ridershift_tenant_id_fleet_member_id_shift_start" to table: "rider_shifts"
CREATE INDEX "ridershift_tenant_id_fleet_member_id_shift_start" ON "rider_shifts" ("tenant_id", "fleet_member_id", "shift_start");
