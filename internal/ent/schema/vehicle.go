package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// Vehicle holds the schema definition for the Vehicle entity.
type Vehicle struct {
	ent.Schema
}

// Fields of the Vehicle.
func (Vehicle) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.UUID("fleet_id", uuid.UUID{}),
		field.String("vehicle_type").
			NotEmpty(),
		field.String("make").
			NotEmpty(),
		field.String("model").
			NotEmpty(),
		field.String("license_plate").
			NotEmpty().
			Unique(),
		field.JSON("capacity_json", map[string]any{}).
			Optional(),
		field.String("status").
			Default("active"),
		field.String("compliance_status").
			Default("pending"),
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

// Edges of the Vehicle.
func (Vehicle) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("fleet", Fleet.Type).
			Ref("vehicles").
			Field("fleet_id").
			Unique().
			Required(),
		edge.From("members", FleetMember.Type).
			Ref("vehicle"),
	}
}

// Indexes of the Vehicle.
func (Vehicle) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "license_plate").Unique(),
	}
}
