package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"github.com/google/uuid"
)

// TaskAssignment holds the schema definition for the TaskAssignment entity.
type TaskAssignment struct {
	ent.Schema
}

// Fields of the TaskAssignment.
func (TaskAssignment) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("task_id", uuid.UUID{}),
		field.UUID("fleet_member_id", uuid.UUID{}),
		field.String("status").
			Default("assigned"),
		field.Time("assigned_at").
			Default(time.Now),
		field.Time("accepted_at").
			Optional().
			Nillable(),
		field.Time("declined_at").
			Optional().
			Nillable(),
		field.Time("completed_at").
			Optional().
			Nillable(),
		field.String("reason_code").
			Optional(),
		field.JSON("metadata", map[string]any{}).
			Default(map[string]any{}),
	}
}

// Edges of the TaskAssignment.
func (TaskAssignment) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("task", Task.Type).
			Ref("assignments").
			Field("task_id").
			Unique().
			Required(),
		edge.From("member", FleetMember.Type).
			Ref("assignments").
			Field("fleet_member_id").
			Unique().
			Required(),
	}
}
