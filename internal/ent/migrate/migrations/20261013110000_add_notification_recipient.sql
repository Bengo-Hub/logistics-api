-- Rider inbox: a notification with recipient_member_id is for that rider only; NULL keeps the
-- dispatcher alert meaning. Nullable column on a populated table, so online auto-migrate adds it
-- without a default.
-- Modify "logistics_notifications" table
ALTER TABLE "logistics_notifications" ADD COLUMN "recipient_member_id" uuid NULL;
-- Create index "logisticsnotification_tenant_id_recipient_member_id_created_at" to table: "logistics_notifications"
CREATE INDEX "logisticsnotification_tenant_id_recipient_member_id_created_at" ON "logistics_notifications" ("tenant_id", "recipient_member_id", "created_at");
