package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"github.com/google/uuid"
)

// TaskStep holds the schema definition for the TaskStep entity.
type TaskStep struct {
	ent.Schema
}

// Fields of the TaskStep.
func (TaskStep) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("task_id", uuid.UUID{}),
		field.String("step_type").
			Comment("pickup | dropoff | hub"),
		field.Int("sequence"),
		field.String("location_name").
			Optional(),
		field.JSON("address_json", map[string]any{}).
			Optional(),
		field.String("contact_name").
			Optional(),
		field.String("contact_phone").
			Optional(),
		field.Bool("requires_signature").
			Default(false),
		field.Bool("requires_photo").
			Default(false),
		field.JSON("metadata", map[string]any{}).
			Default(map[string]any{}),
	}
}

// Edges of the TaskStep.
func (TaskStep) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("task", Task.Type).
			Ref("steps").
			Field("task_id").
			Unique().
			Required(),
	}
}
