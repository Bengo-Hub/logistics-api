-- Add missing columns required by shared-events@v0.2.0 outbox publisher
ALTER TABLE "outbox_events" ADD COLUMN "published_at" timestamptz NULL;
ALTER TABLE "outbox_events" ADD COLUMN "error_message" text NULL;
