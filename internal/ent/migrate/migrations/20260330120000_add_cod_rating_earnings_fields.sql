-- Add COD fields to tasks table
ALTER TABLE "tasks" ADD COLUMN IF NOT EXISTS "cash_on_delivery" double precision DEFAULT 0 NOT NULL;
ALTER TABLE "tasks" ADD COLUMN IF NOT EXISTS "cash_collected" boolean DEFAULT false NOT NULL;

-- Add COD fields to proof_of_deliveries table
ALTER TABLE "proof_of_deliveries" ADD COLUMN IF NOT EXISTS "amount_collected" double precision DEFAULT 0 NOT NULL;
ALTER TABLE "proof_of_deliveries" ADD COLUMN IF NOT EXISTS "collection_method" varchar;

-- Add rating fields to fleet_members table
ALTER TABLE "fleet_members" ADD COLUMN IF NOT EXISTS "average_rating" double precision DEFAULT 0 NOT NULL;
ALTER TABLE "fleet_members" ADD COLUMN IF NOT EXISTS "total_ratings" integer DEFAULT 0 NOT NULL;

-- Create rider_ratings table
CREATE TABLE IF NOT EXISTS "rider_ratings" (
    "id" uuid NOT NULL DEFAULT gen_random_uuid(),
    "tenant_id" uuid NOT NULL,
    "fleet_member_id" uuid NOT NULL,
    "task_id" uuid,
    "order_id" varchar,
    "customer_user_id" varchar,
    "rating" integer NOT NULL CHECK ("rating" >= 1 AND "rating" <= 5),
    "comment" text,
    "created_at" timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY ("id"),
    CONSTRAINT "rider_ratings_fleet_members_ratings" FOREIGN KEY ("fleet_member_id") REFERENCES "fleet_members" ("id") ON DELETE CASCADE
);

-- Indexes for rider_ratings
CREATE INDEX IF NOT EXISTS "riderrating_tenant_id_fleet_member_id" ON "rider_ratings" ("tenant_id", "fleet_member_id");
CREATE INDEX IF NOT EXISTS "riderrating_task_id" ON "rider_ratings" ("task_id");
CREATE INDEX IF NOT EXISTS "riderrating_order_id" ON "rider_ratings" ("order_id");
