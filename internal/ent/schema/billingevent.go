package schema

import (
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/index"
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"github.com/google/uuid"
)

// BillingEvent holds the schema definition for the BillingEvent entity.
type BillingEvent struct {
	ent.Schema
}

// Fields of the BillingEvent.
func (BillingEvent) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.UUID("task_id", uuid.UUID{}).
			Optional(),
		field.String("event_type").
			NotEmpty(),
		field.Float("amount"),
		field.String("currency").
			Default("KES"),
		field.Time("occurred_at").
			Default(time.Now),
		field.JSON("metadata", map[string]any{}).
			Default(map[string]any{}),
	}
}

// Indexes of the BillingEvent.
func (BillingEvent) Indexes() []ent.Index {
	return []ent.Index{
		// Statements, rider totals and the earnings tab filter by tenant, type and time.
		index.Fields("tenant_id", "event_type", "occurred_at"),
		// One rider's events by metadata containment (metadata @> {fleet_member_id}).
		index.Fields("metadata").
			StorageKey("billingevent_metadata_gin").
			Annotations(entsql.IndexType("GIN"), entsql.OpClass("jsonb_path_ops")),
	}
}
