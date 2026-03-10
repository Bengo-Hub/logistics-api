package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"github.com/google/uuid"
)

// TaskEvent holds the schema definition for the TaskEvent entity.
type TaskEvent struct {
	ent.Schema
}

// Fields of the TaskEvent.
func (TaskEvent) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("task_id", uuid.UUID{}),
		field.String("event_type").
			NotEmpty(),
		field.UUID("actor_id", uuid.UUID{}).
			Optional(),
		field.String("actor_type").
			Optional(),
		field.JSON("payload", map[string]any{}).
			Default(map[string]any{}),
		field.Time("occurred_at").
			Default(time.Now),
	}
}

// Edges of the TaskEvent.
func (TaskEvent) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("task", Task.Type).
			Ref("events").
			Field("task_id").
			Unique().
			Required(),
	}
}
