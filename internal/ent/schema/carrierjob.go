package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"github.com/google/uuid"
)

// CarrierJob holds the schema definition for the CarrierJob entity.
type CarrierJob struct {
	ent.Schema
}

// Fields of the CarrierJob.
func (CarrierJob) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.UUID("carrier_id", uuid.UUID{}),
		field.UUID("task_id", uuid.UUID{}),
		field.String("carrier_reference").
			Optional(),
		field.String("status").
			Default("assigned"),
		field.Float("cost_amount").
			Optional(),
		field.String("currency").
			Default("KES"),
		field.Time("assigned_at").
			Default(time.Now),
		field.Time("completed_at").
			Optional().
			Nillable(),
		field.JSON("metadata", map[string]any{}).
			Default(map[string]any{}),
	}
}
