package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"github.com/google/uuid"
)

// TelemetryStream holds the schema definition for the TelemetryStream entity.
type TelemetryStream struct {
	ent.Schema
}

// Fields of the TelemetryStream.
func (TelemetryStream) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.UUID("fleet_member_id", uuid.UUID{}),
		field.String("device_id").
			Optional(),
		field.Time("started_at").
			Default(time.Now),
		field.Time("ended_at").
			Optional().
			Nillable(),
		field.String("status").
			Default("active"),
		field.JSON("metadata", map[string]any{}).
			Default(map[string]any{}),
	}
}

// Edges of the TelemetryStream.
func (TelemetryStream) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("points", TelemetryPoint.Type),
	}
}
