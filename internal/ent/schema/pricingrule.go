package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// PricingRule holds the schema definition for the PricingRule entity.
type PricingRule struct {
	ent.Schema
}

// Fields of the PricingRule.
func (PricingRule) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New).Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.String("name").NotEmpty(),
		field.Enum("rule_type").Values("distance", "weight", "time_of_day", "surge", "flat").Default("distance"),
		field.Float("base_fee").Default(0),
		field.Float("per_km_rate").Optional().Nillable(),
		field.Float("per_kg_rate").Optional().Nillable(),
		field.Float("surge_multiplier").Optional().Nillable(),
		field.JSON("time_windows", []map[string]any{}).Optional().Comment("Peak hour pricing windows"),
		field.JSON("distance_tiers", []map[string]any{}).Optional().Comment("[{max_km: 5, rate: 100}, {max_km: 10, rate: 80}]"),
		field.Int("priority").Default(0).Comment("Higher priority rules override lower"),
		field.Bool("is_active").Default(true),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

// Indexes of the PricingRule.
func (PricingRule) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "is_active"),
		index.Fields("tenant_id", "rule_type"),
	}
}
