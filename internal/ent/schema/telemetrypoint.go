package schema

import (
	"entgo.io/ent/schema/index"
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
		field.Float("temperature_celsius").
			Optional().
			Nillable().
			Comment("Temperature reading from IoT sensor (°C) — used for cold chain monitoring"),
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

// Indexes of the TelemetryPoint.
func (TelemetryPoint) Indexes() []ent.Index {
	return []ent.Index{
		// Latest fix of a stream (fleet map, DISTINCT ON) and a stream's route.
		index.Fields("stream_id", "captured_at"),
		// Daily retention prune by age.
		index.Fields("captured_at"),
	}
}
