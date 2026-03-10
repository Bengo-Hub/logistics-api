-- Create "integration_settings" table
CREATE TABLE "integration_settings" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "tenant_slug" character varying NOT NULL, "service_code" character varying NOT NULL, "config_json" jsonb NOT NULL, "status" character varying NOT NULL DEFAULT 'active', "last_sync_at" timestamptz NULL, "metadata" jsonb NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "integrationsetting_tenant_id_service_code" to table: "integration_settings"
CREATE UNIQUE INDEX "integrationsetting_tenant_id_service_code" ON "integration_settings" ("tenant_id", "service_code");
-- Create index "integrationsetting_tenant_slug" to table: "integration_settings"
CREATE INDEX "integrationsetting_tenant_slug" ON "integration_settings" ("tenant_slug");
-- Create "carrier_jobs" table
CREATE TABLE "carrier_jobs" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "carrier_id" uuid NOT NULL, "task_id" uuid NOT NULL, "carrier_reference" character varying NULL, "status" character varying NOT NULL DEFAULT 'assigned', "cost_amount" double precision NULL, "currency" character varying NOT NULL DEFAULT 'KES', "assigned_at" timestamptz NOT NULL, "completed_at" timestamptz NULL, "metadata" jsonb NOT NULL, PRIMARY KEY ("id"));
-- Create "carrier_partners" table
CREATE TABLE "carrier_partners" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "name" character varying NOT NULL, "provider_code" character varying NOT NULL, "api_credentials_json" jsonb NULL, "status" character varying NOT NULL DEFAULT 'active', "metadata" jsonb NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create "earnings_statements" table
CREATE TABLE "earnings_statements" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "fleet_member_id" uuid NOT NULL, "period_start" timestamptz NOT NULL, "period_end" timestamptz NOT NULL, "gross_amount" double precision NOT NULL, "net_amount" double precision NOT NULL, "bonus_amount" double precision NOT NULL DEFAULT 0, "deduction_amount" double precision NOT NULL DEFAULT 0, "status" character varying NOT NULL DEFAULT 'draft', "generated_at" timestamptz NOT NULL, "metadata" jsonb NOT NULL, PRIMARY KEY ("id"));
-- Create "fleets" table
CREATE TABLE "fleets" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "tenant_slug" character varying NOT NULL, "name" character varying NOT NULL, "type" character varying NOT NULL DEFAULT 'internal', "status" character varying NOT NULL DEFAULT 'active', "metadata" jsonb NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "fleet_tenant_id" to table: "fleets"
CREATE INDEX "fleet_tenant_id" ON "fleets" ("tenant_id");
-- Create index "fleet_tenant_slug" to table: "fleets"
CREATE INDEX "fleet_tenant_slug" ON "fleets" ("tenant_slug");
-- Create "outbox_events" table
CREATE TABLE "outbox_events" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "aggregate_type" character varying NOT NULL, "aggregate_id" uuid NOT NULL, "event_type" character varying NOT NULL, "payload" jsonb NOT NULL, "status" character varying NOT NULL DEFAULT 'pending', "attempts" bigint NOT NULL DEFAULT 0, "last_attempt_at" timestamptz NULL, "created_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "outboxevent_status_created_at" to table: "outbox_events"
CREATE INDEX "outboxevent_status_created_at" ON "outbox_events" ("status", "created_at");
-- Create index "outboxevent_tenant_id_status" to table: "outbox_events"
CREATE INDEX "outboxevent_tenant_id_status" ON "outbox_events" ("tenant_id", "status");
-- Create "billing_events" table
CREATE TABLE "billing_events" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "task_id" uuid NULL, "event_type" character varying NOT NULL, "amount" double precision NOT NULL, "currency" character varying NOT NULL DEFAULT 'KES', "occurred_at" timestamptz NOT NULL, "metadata" jsonb NOT NULL, PRIMARY KEY ("id"));
-- Create "tenant_sync_events" table
CREATE TABLE "tenant_sync_events" ("id" uuid NOT NULL, "tenant_id" uuid NULL, "tenant_slug" character varying NOT NULL, "source_service" character varying NOT NULL, "payload" jsonb NOT NULL, "synced_at" timestamptz NOT NULL, "status" character varying NOT NULL DEFAULT 'processed', PRIMARY KEY ("id"));
-- Create "tenants" table
CREATE TABLE "tenants" ("id" uuid NOT NULL, "name" character varying NOT NULL, "slug" character varying NOT NULL, "status" character varying NOT NULL DEFAULT 'active', "contact_email" character varying NULL, "contact_phone" character varying NULL, "logo_url" character varying NULL, "website" character varying NULL, "country" character varying NULL DEFAULT 'KE', "timezone" character varying NULL DEFAULT 'Africa/Nairobi', "brand_colors" jsonb NULL, "org_size" character varying NULL, "use_case" character varying NULL, "subscription_plan" character varying NULL, "subscription_status" character varying NULL, "subscription_expires_at" timestamptz NULL, "subscription_id" character varying NULL, "tier_limits" jsonb NULL, "metadata" jsonb NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "tenant_slug" to table: "tenants"
CREATE UNIQUE INDEX "tenant_slug" ON "tenants" ("slug");
-- Create index "tenant_status" to table: "tenants"
CREATE INDEX "tenant_status" ON "tenants" ("status");
-- Create index "tenants_slug_key" to table: "tenants"
CREATE UNIQUE INDEX "tenants_slug_key" ON "tenants" ("slug");
-- Create "users" table
CREATE TABLE "users" ("id" uuid NOT NULL, "auth_service_user_id" uuid NULL, "email" character varying NOT NULL, "sync_status" character varying NOT NULL DEFAULT 'pending', "sync_at" timestamptz NULL, "full_name" character varying NOT NULL, "phone" character varying NULL, "status" character varying NOT NULL DEFAULT 'active', "role" character varying NULL, "metadata" jsonb NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "tenant_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "users_tenants_users" FOREIGN KEY ("tenant_id") REFERENCES "tenants" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create index "user_email" to table: "users"
CREATE INDEX "user_email" ON "users" ("email");
-- Create index "user_sync_status" to table: "users"
CREATE INDEX "user_sync_status" ON "users" ("sync_status");
-- Create index "user_tenant_id_auth_service_user_id" to table: "users"
CREATE INDEX "user_tenant_id_auth_service_user_id" ON "users" ("tenant_id", "auth_service_user_id");
-- Create index "user_tenant_id_email" to table: "users"
CREATE UNIQUE INDEX "user_tenant_id_email" ON "users" ("tenant_id", "email");
-- Create index "users_auth_service_user_id_key" to table: "users"
CREATE UNIQUE INDEX "users_auth_service_user_id_key" ON "users" ("auth_service_user_id");
-- Create "vehicles" table
CREATE TABLE "vehicles" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "vehicle_type" character varying NOT NULL, "make" character varying NOT NULL, "model" character varying NOT NULL, "license_plate" character varying NOT NULL, "capacity_json" jsonb NULL, "status" character varying NOT NULL DEFAULT 'active', "compliance_status" character varying NOT NULL DEFAULT 'pending', "metadata" jsonb NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "fleet_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "vehicles_fleets_vehicles" FOREIGN KEY ("fleet_id") REFERENCES "fleets" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create index "vehicle_tenant_id_license_plate" to table: "vehicles"
CREATE UNIQUE INDEX "vehicle_tenant_id_license_plate" ON "vehicles" ("tenant_id", "license_plate");
-- Create index "vehicles_license_plate_key" to table: "vehicles"
CREATE UNIQUE INDEX "vehicles_license_plate_key" ON "vehicles" ("license_plate");
-- Create "fleet_members" table
CREATE TABLE "fleet_members" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "driver_code" character varying NULL, "status" character varying NOT NULL DEFAULT 'active', "joined_at" timestamptz NOT NULL, "suspended_at" timestamptz NULL, "metadata" jsonb NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "fleet_id" uuid NOT NULL, "vehicle_id" uuid NULL, "user_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "fleet_members_fleets_members" FOREIGN KEY ("fleet_id") REFERENCES "fleets" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION, CONSTRAINT "fleet_members_users_fleet_memberships" FOREIGN KEY ("user_id") REFERENCES "users" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION, CONSTRAINT "fleet_members_vehicles_vehicle" FOREIGN KEY ("vehicle_id") REFERENCES "vehicles" ("id") ON UPDATE NO ACTION ON DELETE SET NULL);
-- Create index "fleet_members_driver_code_key" to table: "fleet_members"
CREATE UNIQUE INDEX "fleet_members_driver_code_key" ON "fleet_members" ("driver_code");
-- Create index "fleetmember_driver_code" to table: "fleet_members"
CREATE UNIQUE INDEX "fleetmember_driver_code" ON "fleet_members" ("driver_code");
-- Create index "fleetmember_tenant_id_user_id" to table: "fleet_members"
CREATE UNIQUE INDEX "fleetmember_tenant_id_user_id" ON "fleet_members" ("tenant_id", "user_id");
-- Create "tasks" table
CREATE TABLE "tasks" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "external_reference" character varying NULL, "source_service" character varying NULL, "task_type" character varying NOT NULL DEFAULT 'delivery', "priority" bigint NOT NULL DEFAULT 0, "status" character varying NOT NULL DEFAULT 'pending', "sla_due_at" timestamptz NULL, "requested_pickup_at" timestamptz NULL, "requested_dropoff_at" timestamptz NULL, "metadata" jsonb NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "user_tasks" uuid NULL, PRIMARY KEY ("id"), CONSTRAINT "tasks_users_tasks" FOREIGN KEY ("user_tasks") REFERENCES "users" ("id") ON UPDATE NO ACTION ON DELETE SET NULL);
-- Create index "task_status" to table: "tasks"
CREATE INDEX "task_status" ON "tasks" ("status");
-- Create index "task_tenant_id_external_reference" to table: "tasks"
CREATE INDEX "task_tenant_id_external_reference" ON "tasks" ("tenant_id", "external_reference");
-- Create "proof_of_deliveries" table
CREATE TABLE "proof_of_deliveries" ("id" uuid NOT NULL, "fleet_member_id" uuid NOT NULL, "signature_url" character varying NULL, "photo_url" character varying NULL, "otp_code" character varying NULL, "captured_at" timestamptz NOT NULL, "metadata" jsonb NOT NULL, "task_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "proof_of_deliveries_tasks_proof_of_delivery" FOREIGN KEY ("task_id") REFERENCES "tasks" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create index "proof_of_deliveries_task_id_key" to table: "proof_of_deliveries"
CREATE UNIQUE INDEX "proof_of_deliveries_task_id_key" ON "proof_of_deliveries" ("task_id");
-- Create "task_assignments" table
CREATE TABLE "task_assignments" ("id" uuid NOT NULL, "status" character varying NOT NULL DEFAULT 'assigned', "assigned_at" timestamptz NOT NULL, "accepted_at" timestamptz NULL, "declined_at" timestamptz NULL, "completed_at" timestamptz NULL, "reason_code" character varying NULL, "metadata" jsonb NOT NULL, "fleet_member_id" uuid NOT NULL, "task_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "task_assignments_fleet_members_assignments" FOREIGN KEY ("fleet_member_id") REFERENCES "fleet_members" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION, CONSTRAINT "task_assignments_tasks_assignments" FOREIGN KEY ("task_id") REFERENCES "tasks" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create "task_events" table
CREATE TABLE "task_events" ("id" uuid NOT NULL, "event_type" character varying NOT NULL, "actor_id" uuid NULL, "actor_type" character varying NULL, "payload" jsonb NOT NULL, "occurred_at" timestamptz NOT NULL, "task_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "task_events_tasks_events" FOREIGN KEY ("task_id") REFERENCES "tasks" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create "task_steps" table
CREATE TABLE "task_steps" ("id" uuid NOT NULL, "step_type" character varying NOT NULL, "sequence" bigint NOT NULL, "location_name" character varying NULL, "address_json" jsonb NULL, "contact_name" character varying NULL, "contact_phone" character varying NULL, "requires_signature" boolean NOT NULL DEFAULT false, "requires_photo" boolean NOT NULL DEFAULT false, "metadata" jsonb NOT NULL, "task_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "task_steps_tasks_steps" FOREIGN KEY ("task_id") REFERENCES "tasks" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create "telemetry_streams" table
CREATE TABLE "telemetry_streams" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "fleet_member_id" uuid NOT NULL, "device_id" character varying NULL, "started_at" timestamptz NOT NULL, "ended_at" timestamptz NULL, "status" character varying NOT NULL DEFAULT 'active', "metadata" jsonb NOT NULL, PRIMARY KEY ("id"));
-- Create "telemetry_points" table
CREATE TABLE "telemetry_points" ("id" uuid NOT NULL, "captured_at" timestamptz NOT NULL, "speed_kph" double precision NULL, "bearing_deg" double precision NULL, "accuracy_m" double precision NULL, "altitude_m" double precision NULL, "battery_pct" double precision NULL, "metadata" jsonb NOT NULL, "stream_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "telemetry_points_telemetry_streams_points" FOREIGN KEY ("stream_id") REFERENCES "telemetry_streams" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
