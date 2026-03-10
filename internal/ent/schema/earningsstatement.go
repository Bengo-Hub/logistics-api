package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"github.com/google/uuid"
)

// EarningsStatement holds the schema definition for the EarningsStatement entity.
type EarningsStatement struct {
	ent.Schema
}

// Fields of the EarningsStatement.
func (EarningsStatement) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.UUID("fleet_member_id", uuid.UUID{}),
		field.Time("period_start"),
		field.Time("period_end"),
		field.Float("gross_amount"),
		field.Float("net_amount"),
		field.Float("bonus_amount").
			Default(0),
		field.Float("deduction_amount").
			Default(0),
		field.String("status").
			Default("draft"),
		field.Time("generated_at").
			Default(time.Now),
		field.JSON("metadata", map[string]any{}).
			Default(map[string]any{}),
	}
}
