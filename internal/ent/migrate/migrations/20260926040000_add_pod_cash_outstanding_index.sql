-- Create index "proofofdelivery_cash_outstanding" to table: "proof_of_deliveries"
CREATE INDEX "proofofdelivery_cash_outstanding" ON "proof_of_deliveries" ("tenant_id", "fleet_member_id", "captured_at") WHERE ((collection_method)::text = 'cash'::text AND amount_collected > (0)::double precision AND ((metadata ->> 'remitted_at'::text) IS NULL));
