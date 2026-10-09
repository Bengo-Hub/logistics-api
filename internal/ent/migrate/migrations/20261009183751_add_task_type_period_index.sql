-- Create index "task_tenant_id_task_type_created_at" to table: "tasks"
CREATE INDEX "task_tenant_id_task_type_created_at" ON "tasks" ("tenant_id", "task_type", "created_at");
