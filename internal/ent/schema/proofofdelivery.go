package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
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
		field.UUID("tenant_id", uuid.UUID{}).
			Comment("Tenant isolation — ensures PoD records cannot leak across tenants"),
		field.UUID("task_id", uuid.UUID{}),
		field.UUID("fleet_member_id", uuid.UUID{}),
		field.String("signature_url").
			Optional(),
		field.String("photo_url").
			Optional(),
		field.String("otp_code").
			Optional(),
		field.Float("amount_collected").
			Default(0).
			Comment("Cash amount collected from customer (for COD orders)"),
		field.String("collection_method").
			Optional().
			Comment("How cash was collected: cash, mobile_money"),
		field.Time("captured_at").
			Default(time.Now),
		// Hospital / KEMSA multi-step PoD fields
		field.String("receiving_staff_name").
			Optional().
			Comment("Name of hospital/facility staff who received the delivery"),
		field.String("receiving_staff_signature_url").
			Optional().
			Comment("Digital signature of receiving staff"),
		field.String("condition_on_arrival").
			Optional().
			Comment("good | damaged | temperature_breach"),
		field.Int("received_quantity").
			Optional().
			Nillable().
			Comment("Quantity verified at destination (for distribution deliveries)"),
		field.String("batch_reference").
			Optional().
			Comment("Links to shipment_code for KEMSA batch tracking"),
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

// Indexes of the ProofOfDelivery.
func (ProofOfDelivery) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id"),
		index.Fields("tenant_id", "task_id"),
	}
}
