-- Create index "taskassignment_task_id_status" to table: "task_assignments"
CREATE INDEX "taskassignment_task_id_status" ON "task_assignments" ("task_id", "status");
-- Create index "taskassignment_fleet_member_id_status" to table: "task_assignments"
CREATE INDEX "taskassignment_fleet_member_id_status" ON "task_assignments" ("fleet_member_id", "status");
-- Create index "taskstep_task_id" to table: "task_steps"
CREATE INDEX "taskstep_task_id" ON "task_steps" ("task_id");
-- Create index "taskevent_task_id_occurred_at" to table: "task_events"
CREATE INDEX "taskevent_task_id_occurred_at" ON "task_events" ("task_id", "occurred_at");
-- Create index "telemetrypoint_stream_id_captured_at" to table: "telemetry_points"
CREATE INDEX "telemetrypoint_stream_id_captured_at" ON "telemetry_points" ("stream_id", "captured_at");
-- Create index "telemetrypoint_captured_at" to table: "telemetry_points"
CREATE INDEX "telemetrypoint_captured_at" ON "telemetry_points" ("captured_at");
-- Create index "telemetrystream_tenant_id_fleet_member_id_status" to table: "telemetry_streams"
CREATE INDEX "telemetrystream_tenant_id_fleet_member_id_status" ON "telemetry_streams" ("tenant_id", "fleet_member_id", "status");
-- Create index "billingevent_tenant_id_event_type_occurred_at" to table: "billing_events"
CREATE INDEX "billingevent_tenant_id_event_type_occurred_at" ON "billing_events" ("tenant_id", "event_type", "occurred_at");
-- Create index "billingevent_metadata_gin" to table: "billing_events"
CREATE INDEX "billingevent_metadata_gin" ON "billing_events" USING GIN ("metadata" jsonb_path_ops);
-- Modify index "task_sla_open": delivered tasks are closed too
DROP INDEX "task_sla_open";
CREATE INDEX "task_sla_open" ON "tasks" ("sla_due_at") WHERE ((sla_due_at IS NOT NULL) AND ((status)::text <> ALL ((ARRAY['delivered'::character varying, 'completed'::character varying, 'cancelled'::character varying, 'failed'::character varying, 'returned'::character varying])::text[])));
