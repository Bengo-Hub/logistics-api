-- Modify "proof_of_deliveries" table
ALTER TABLE "proof_of_deliveries" ADD COLUMN "tenant_id" uuid NOT NULL;
-- Create index "proofofdelivery_tenant_id" to table: "proof_of_deliveries"
CREATE INDEX "proofofdelivery_tenant_id" ON "proof_of_deliveries" ("tenant_id");
-- Create index "proofofdelivery_tenant_id_task_id" to table: "proof_of_deliveries"
CREATE INDEX "proofofdelivery_tenant_id_task_id" ON "proof_of_deliveries" ("tenant_id", "task_id");
