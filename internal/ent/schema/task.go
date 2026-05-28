package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// Task holds the schema definition for the Task entity.
type Task struct {
	ent.Schema
}

// Fields of the Task.
func (Task) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.String("tracking_code").
			MaxLen(20).
			Optional().
			Unique().
			Comment("Public tracking code (waybill) e.g. CV-20260320-A3F8K2"),
		field.String("external_reference").
			Optional().
			Comment("Reference to upstream orders/transfers"),
		field.String("source_service").
			Optional().
			Comment("ordering-backend | inventory-service | pos-service"),
		field.String("task_type").
			Default("delivery").
			Comment("food_delivery | retail_delivery | outlet_transfer | commercial_courier | drop_shipping | pickup | return | ride"),
		field.Int("priority").
			Default(0),
		field.String("status").
			Default("pending"),
		field.Time("sla_due_at").
			Optional().
			Nillable(),
		field.Time("requested_pickup_at").
			Optional().
			Nillable(),
		field.Time("requested_dropoff_at").
			Optional().
			Nillable(),
		field.JSON("metadata", map[string]any{}).
			Default(map[string]any{}),
		field.Float("package_weight_kg").
			Optional().
			Nillable().
			Comment("Package weight for pricing/rider matching"),
		field.JSON("package_dimensions_cm", map[string]float64{}).
			Optional().
			Comment("Package dimensions {length, width, height} cm"),
		field.Bool("requires_temperature_control").
			Default(false).
			Comment("Food/pharma cold chain requirement"),
		field.String("temperature_range").
			Optional().
			Comment("ambient, chilled, frozen"),
		field.Bool("requires_fragile_handling").
			Default(false).
			Comment("Fragile goods handling"),
		field.Bool("requires_heavy_duty").
			Default(false).
			Comment("Heavy items: furniture, hardware, building materials"),
		field.Float("cash_on_delivery").
			Default(0).
			Comment("Amount to collect in cash from the customer (0 = prepaid)"),
		field.Bool("cash_collected").
			Default(false).
			Comment("Whether COD cash has been collected by the rider"),
		field.UUID("outlet_id", uuid.UUID{}).
			Optional().
			Nillable().
			Comment("Dispatch outlet for this task"),
		field.String("carrier_id").
			Optional().
			Comment("External carrier reference if outsourced to 3rd party"),
		field.UUID("shipment_id", uuid.UUID{}).
			Optional().
			Nillable().
			Comment("Shipment batch this task belongs to (KEMSA distribution)"),
		field.String("seal_number").
			Optional().
			Comment("Physical seal/lock number applied at dispatch — for KEMSA chain-of-custody"),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
	}
}

// Edges of the Task.
func (Task) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("steps", TaskStep.Type),
		edge.To("events", TaskEvent.Type),
		edge.To("assignments", TaskAssignment.Type),
		edge.To("proof_of_delivery", ProofOfDelivery.Type).Unique(),
	}
}

// Indexes of the Task.
func (Task) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "external_reference"),
		index.Fields("status"),
		index.Fields("tracking_code"),
		index.Fields("tenant_id", "status", "created_at"),
		index.Fields("tenant_id", "outlet_id", "created_at"),
	}
}
