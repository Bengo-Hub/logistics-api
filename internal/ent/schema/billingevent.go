package schema

import (
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
