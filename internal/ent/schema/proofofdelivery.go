package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"github.com/google/uuid"
)

// ProofOfDelivery holds the schema definition for the ProofOfDelivery entity.
type ProofOfDelivery struct {
	ent.Schema
}

// Fields of the ProofOfDelivery.
func (ProofOfDelivery) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("task_id", uuid.UUID{}),
		field.UUID("fleet_member_id", uuid.UUID{}),
		field.String("signature_url").
			Optional(),
		field.String("photo_url").
			Optional(),
		field.String("otp_code").
			Optional(),
		field.Time("captured_at").
			Default(time.Now),
		field.JSON("metadata", map[string]any{}).
			Default(map[string]any{}),
	}
}

// Edges of the ProofOfDelivery.
func (ProofOfDelivery) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("task", Task.Type).
			Ref("proof_of_delivery").
			Field("task_id").
			Unique().
			Required(),
	}
}
