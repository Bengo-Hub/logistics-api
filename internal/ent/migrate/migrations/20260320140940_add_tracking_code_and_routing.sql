-- Modify "tasks" table
ALTER TABLE "tasks" ADD COLUMN "tracking_code" character varying NULL;
-- Create index "task_tracking_code" to table: "tasks"
CREATE INDEX "task_tracking_code" ON "tasks" ("tracking_code");
-- Create index "tasks_tracking_code_key" to table: "tasks"
CREATE UNIQUE INDEX "tasks_tracking_code_key" ON "tasks" ("tracking_code");
