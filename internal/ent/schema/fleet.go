package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// Fleet holds the schema definition for the Fleet entity.
type Fleet struct {
	ent.Schema
}

// Fields of the Fleet.
func (Fleet) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.String("tenant_slug").
			NotEmpty(),
		field.String("name").
			NotEmpty(),
		field.String("type").
			Default("internal").
			Comment("internal | third-party"),
		field.String("status").
			Default("active"),
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

// Edges of the Fleet.
func (Fleet) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("members", FleetMember.Type),
		edge.To("vehicles", Vehicle.Type),
	}
}

// Indexes of the Fleet.
func (Fleet) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id"),
		index.Fields("tenant_slug"),
	}
}
