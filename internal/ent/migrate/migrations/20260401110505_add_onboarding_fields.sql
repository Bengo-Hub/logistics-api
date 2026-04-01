-- Modify "fleet_members" table
ALTER TABLE "fleet_members" ALTER COLUMN "status" SET DEFAULT 'invited', ALTER COLUMN "total_ratings" TYPE bigint, ADD COLUMN "invite_code" character varying NULL, ADD COLUMN "kyc_submitted_at" timestamptz NULL, ADD COLUMN "reviewed_at" timestamptz NULL, ADD COLUMN "reviewed_by" character varying NULL, ADD COLUMN "rejection_reason" character varying NULL, ADD COLUMN "onboarding_source" character varying NULL DEFAULT 'invite';
-- Create index "fleet_members_invite_code_key" to table: "fleet_members"
CREATE UNIQUE INDEX "fleet_members_invite_code_key" ON "fleet_members" ("invite_code");
-- Modify "fleets" table
ALTER TABLE "fleets" ADD COLUMN "fleet_type" character varying NOT NULL DEFAULT 'delivery';
-- Modify "outbox_events" table
ALTER TABLE "outbox_events" ALTER COLUMN "error_message" TYPE character varying;
-- Modify "vehicles" table
ALTER TABLE "vehicles" ADD COLUMN "insurance_expiry" timestamptz NULL, ADD COLUMN "insurance_document" character varying NULL, ADD COLUMN "inspection_expiry" timestamptz NULL, ADD COLUMN "inspection_document" character varying NULL;
-- Modify "rider_ratings" table
ALTER TABLE "rider_ratings" DROP CONSTRAINT "rider_ratings_rating_check", DROP CONSTRAINT "rider_ratings_fleet_members_ratings", ALTER COLUMN "id" DROP DEFAULT, ALTER COLUMN "rating" TYPE bigint, ALTER COLUMN "created_at" DROP DEFAULT, ADD CONSTRAINT "rider_ratings_fleet_members_ratings" FOREIGN KEY ("fleet_member_id") REFERENCES "fleet_members" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION;
