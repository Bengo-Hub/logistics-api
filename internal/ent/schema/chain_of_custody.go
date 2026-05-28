package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// ChainOfCustody records each handoff/event in a shipment's audit trail.
// Every seal, release, temperature reading, and signature is an immutable record.
type ChainOfCustody struct {
	ent.Schema
}

// Fields of the ChainOfCustody.
func (ChainOfCustody) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("shipment_id", uuid.UUID{}),
		field.UUID("task_id", uuid.UUID{}).
			Optional().
			Nillable().
			Comment("Optional: the specific task within the shipment this event relates to"),
		field.UUID("actor_id", uuid.UUID{}).
			Comment("FleetMember ID who performed the action"),
		field.String("actor_name").
			Comment("Denormalised name for audit readability"),
		field.String("event_type").
			Comment("released | received | sealed | unsealed | temperature_breach | damaged | partial"),
		field.String("location_name").
			Optional(),
		field.Float("latitude").
			Optional().
			Nillable(),
		field.Float("longitude").
			Optional().
			Nillable(),
		field.String("notes").
			Optional(),
		field.String("photo_url").
			Optional().
			Comment("PoD photo or damage photo URL"),
		field.String("signature_url").
			Optional().
			Comment("Digital signature of receiving party"),
		field.Float("temperature_reading").
			Optional().
			Nillable().
			Comment("Temperature reading at time of event (°C)"),
		field.Int("received_quantity").
			Optional().
			Nillable().
			Comment("Quantity received/verified at destination"),
		field.String("receiving_staff_name").
			Optional().
			Comment("Hospital/facility staff who received the shipment"),
		field.Time("occurred_at").
			Default(time.Now).
			Immutable(),
	}
}

// Edges of the ChainOfCustody.
func (ChainOfCustody) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("shipment", Shipment.Type).
			Ref("chain_of_custody").
			Field("shipment_id").
			Required().
			Unique(),
	}
}

// Indexes of the ChainOfCustody.
func (ChainOfCustody) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("shipment_id", "occurred_at"),
		index.Fields("task_id"),
	}
}
