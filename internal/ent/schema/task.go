package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// Task holds the schema definition for the Task entity.
type Task struct {
	ent.Schema
}

// Fields of the Task.
func (Task) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.String("external_reference").
			Optional().
			Comment("Reference to upstream orders/transfers"),
		field.String("source_service").
			Optional().
			Comment("cafe-backend | inventory-service | pos-service"),
		field.String("task_type").
			Default("delivery").
			Comment("delivery | pickup | return | transfer"),
		field.Int("priority").
			Default(0),
		field.String("status").
			Default("pending"),
		field.Time("sla_due_at").
			Optional().
			Nillable(),
		field.Time("requested_pickup_at").
			Optional().
			Nillable(),
		field.Time("requested_dropoff_at").
			Optional().
			Nillable(),
		field.JSON("metadata", map[string]any{}).
			Default(map[string]any{}),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
	}
}

// Edges of the Task.
func (Task) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("steps", TaskStep.Type),
		edge.To("events", TaskEvent.Type),
		edge.To("assignments", TaskAssignment.Type),
		edge.To("proof_of_delivery", ProofOfDelivery.Type).Unique(),
	}
}

// Indexes of the Task.
func (Task) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "external_reference"),
		index.Fields("status"),
	}
}
