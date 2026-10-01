-- Create index "task_sla_open" to table: "tasks"
CREATE INDEX "task_sla_open" ON "tasks" ("sla_due_at") WHERE ((sla_due_at IS NOT NULL) AND ((status)::text <> ALL ((ARRAY['completed'::character varying, 'cancelled'::character varying, 'failed'::character varying, 'returned'::character varying])::text[])));
