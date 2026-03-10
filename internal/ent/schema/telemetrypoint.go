package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"github.com/google/uuid"
)

// TelemetryPoint holds the schema definition for the TelemetryPoint entity.
type TelemetryPoint struct {
	ent.Schema
}

// Fields of the TelemetryPoint.
func (TelemetryPoint) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("stream_id", uuid.UUID{}),
		field.Time("captured_at").
			Default(time.Now),
		field.Float("speed_kph").
			Optional(),
		field.Float("bearing_deg").
			Optional(),
		field.Float("accuracy_m").
			Optional(),
		field.Float("altitude_m").
			Optional(),
		field.Float("battery_pct").
			Optional(),
		field.JSON("metadata", map[string]any{}).
			Default(map[string]any{}),
	}
}

// Edges of the TelemetryPoint.
func (TelemetryPoint) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("stream", TelemetryStream.Type).
			Ref("points").
			Field("stream_id").
			Unique().
			Required(),
	}
}
