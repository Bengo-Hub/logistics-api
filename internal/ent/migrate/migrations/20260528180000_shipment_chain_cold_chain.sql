-- Create "shipments" table
CREATE TABLE "shipments" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "shipment_code" character varying NOT NULL, "shipment_type" character varying NOT NULL DEFAULT 'warehouse_transfer', "status" character varying NOT NULL DEFAULT 'planned', "fleet_type" character varying NOT NULL DEFAULT 'distribution', "source_facility_id" uuid NULL, "source_facility_name" character varying NULL, "dest_facility_id" uuid NULL, "dest_facility_name" character varying NULL, "temperature_min_celsius" double precision NULL, "temperature_max_celsius" double precision NULL, "special_handling" jsonb NULL, "seal_number" character varying NULL, "planned_dispatch_at" timestamptz NULL, "dispatched_at" timestamptz NULL, "completed_at" timestamptz NULL, "external_reference" character varying NULL, "metadata" jsonb NOT NULL DEFAULT '{}', "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "shipment_shipment_code" to table: "shipments"
CREATE UNIQUE INDEX "shipment_shipment_code" ON "shipments" ("shipment_code");
-- Create index "shipment_tenant_id_status_created_at" to table: "shipments"
CREATE INDEX "shipment_tenant_id_status_created_at" ON "shipments" ("tenant_id", "status", "created_at");
-- Create index "shipment_external_reference" to table: "shipments"
CREATE INDEX "shipment_external_reference" ON "shipments" ("external_reference");
-- Create "chain_of_custodies" table
CREATE TABLE "chain_of_custodies" ("id" uuid NOT NULL, "shipment_id" uuid NOT NULL, "task_id" uuid NULL, "actor_id" uuid NOT NULL, "actor_name" character varying NOT NULL, "event_type" character varying NOT NULL, "location_name" character varying NULL, "latitude" double precision NULL, "longitude" double precision NULL, "notes" character varying NULL, "photo_url" character varying NULL, "signature_url" character varying NULL, "temperature_reading" double precision NULL, "received_quantity" bigint NULL, "receiving_staff_name" character varying NULL, "occurred_at" timestamptz NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "chain_of_custodies_shipments_chain_of_custody" FOREIGN KEY ("shipment_id") REFERENCES "shipments" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create index "chainofcustody_shipment_id_occurred_at" to table: "chain_of_custodies"
CREATE INDEX "chainofcustody_shipment_id_occurred_at" ON "chain_of_custodies" ("shipment_id", "occurred_at");
-- Create index "chainofcustody_task_id" to table: "chain_of_custodies"
CREATE INDEX "chainofcustody_task_id" ON "chain_of_custodies" ("task_id");
-- Add "shipment_id" column to "tasks" table
ALTER TABLE "tasks" ADD COLUMN "shipment_id" uuid NULL;
-- Add "seal_number" column to "tasks" table
ALTER TABLE "tasks" ADD COLUMN "seal_number" character varying NULL;
-- Add "temperature_celsius" column to "telemetry_points" table
ALTER TABLE "telemetry_points" ADD COLUMN "temperature_celsius" double precision NULL;
-- Add hospital PoD fields to "proof_of_deliveries" table
ALTER TABLE "proof_of_deliveries" ADD COLUMN "receiving_staff_name" character varying NULL;
ALTER TABLE "proof_of_deliveries" ADD COLUMN "receiving_staff_signature_url" character varying NULL;
ALTER TABLE "proof_of_deliveries" ADD COLUMN "condition_on_arrival" character varying NULL;
ALTER TABLE "proof_of_deliveries" ADD COLUMN "received_quantity" bigint NULL;
ALTER TABLE "proof_of_deliveries" ADD COLUMN "batch_reference" character varying NULL;
