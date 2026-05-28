-- Create "outlets" table
CREATE TABLE "outlets" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "code" character varying NOT NULL, "name" character varying NOT NULL, "use_case" character varying NOT NULL DEFAULT 'logistics', "address" character varying NULL, "latitude" double precision NULL, "longitude" double precision NULL, "timezone" character varying NULL DEFAULT 'Africa/Nairobi', "is_hq" boolean NOT NULL DEFAULT false, "status" character varying NOT NULL DEFAULT 'active', "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "outlet_tenant_id_code" to table: "outlets"
CREATE UNIQUE INDEX "outlet_tenant_id_code" ON "outlets" ("tenant_id", "code");
-- Create index "outlet_tenant_id_status" to table: "outlets"
CREATE INDEX "outlet_tenant_id_status" ON "outlets" ("tenant_id", "status");
