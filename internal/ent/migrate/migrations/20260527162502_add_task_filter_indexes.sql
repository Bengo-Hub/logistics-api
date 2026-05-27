-- Create index "task_tenant_id_outlet_id_created_at" to table: "tasks"
CREATE INDEX "task_tenant_id_outlet_id_created_at" ON "tasks" ("tenant_id", "outlet_id", "created_at");
-- Create index "task_tenant_id_status_created_at" to table: "tasks"
CREATE INDEX "task_tenant_id_status_created_at" ON "tasks" ("tenant_id", "status", "created_at");
