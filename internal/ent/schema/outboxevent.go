package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// OutboxEvent holds the schema definition for the OutboxEvent entity.
type OutboxEvent struct {
	ent.Schema
}

// Fields of the OutboxEvent.
func (OutboxEvent) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.String("aggregate_type").
			NotEmpty(),
		field.UUID("aggregate_id", uuid.UUID{}),
		field.String("event_type").
			NotEmpty(),
		field.JSON("payload", map[string]any{}),
		field.String("status").
			Default("pending"),
		field.Int("attempts").
			Default(0),
		field.Time("last_attempt_at").
			Optional().
			Nillable(),
		field.Time("published_at").
			Optional().
			Nillable(),
		field.String("error_message").
			Optional().
			Nillable(),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
	}
}

// Indexes of the OutboxEvent.
func (OutboxEvent) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "status"),
		index.Fields("status", "created_at"),
	}
}
