package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// GeoFence holds the schema definition for delivery zones.
type GeoFence struct {
	ent.Schema
}

// Fields of the GeoFence.
func (GeoFence) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.String("name").
			NotEmpty().
			Comment("Human-readable zone name e.g. 'CBD & Downtown'"),
		field.String("zone_type").
			Default("delivery").
			Comment("delivery | pickup | exclusion | surge"),
		field.String("status").
			Default("active").
			Comment("active | inactive | draft"),
		field.JSON("boundary", [][]float64{}).
			Comment("Polygon boundary as [[lng, lat], ...] coordinate pairs"),
		field.String("color").
			Default("#3b82f6").
			Optional().
			Comment("Hex color for map display"),
		field.JSON("metadata", map[string]any{}).
			Default(map[string]any{}).
			Optional(),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
	}
}

// Indexes of the GeoFence.
func (GeoFence) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id"),
		index.Fields("tenant_id", "name").Unique(),
		index.Fields("status"),
	}
}
