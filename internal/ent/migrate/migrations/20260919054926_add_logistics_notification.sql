-- Create "logistics_notifications" table
CREATE TABLE "logistics_notifications" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "notification_type" character varying NOT NULL, "title" character varying NOT NULL, "body" text NULL, "payload" jsonb NOT NULL, "related_task_id" uuid NULL, "is_read" boolean NOT NULL DEFAULT false, "created_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "logisticsnotification_tenant_i_1241608ab04e76d047885e3c97f2a491" to table: "logistics_notifications"
CREATE INDEX "logisticsnotification_tenant_i_1241608ab04e76d047885e3c97f2a491" ON "logistics_notifications" ("tenant_id", "notification_type", "related_task_id");
-- Create index "logisticsnotification_tenant_id_is_read_created_at" to table: "logistics_notifications"
CREATE INDEX "logisticsnotification_tenant_id_is_read_created_at" ON "logistics_notifications" ("tenant_id", "is_read", "created_at");
