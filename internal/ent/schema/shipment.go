package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// Shipment represents a distribution batch — a group of tasks dispatched together.
// Used primarily for the KEMSA/medical distribution use case (warehouse → hospital delivery).
type Shipment struct {
	ent.Schema
}

// Fields of the Shipment.
func (Shipment) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.String("shipment_code").
			Comment("Human-readable code e.g. SHIP-2026-001 (uniqueness enforced by the shipment_code index)"),
		field.String("shipment_type").
			Default("warehouse_transfer").
			Comment("warehouse_transfer | hospital_delivery | recall"),
		field.String("status").
			Default("planned").
			Comment("planned | in_transit | partially_delivered | completed | cancelled"),
		field.String("fleet_type").
			Default("distribution").
			Comment("distribution | courier"),
		field.UUID("source_facility_id", uuid.UUID{}).
			Optional().
			Nillable().
			Comment("Source warehouse/outlet UUID (references auth-api outlet)"),
		field.String("source_facility_name").
			Optional(),
		field.UUID("dest_facility_id", uuid.UUID{}).
			Optional().
			Nillable().
			Comment("Destination facility UUID (hospital, warehouse)"),
		field.String("dest_facility_name").
			Optional(),
		field.Float("temperature_min_celsius").
			Optional().
			Nillable().
			Comment("Cold chain: minimum acceptable temperature"),
		field.Float("temperature_max_celsius").
			Optional().
			Nillable().
			Comment("Cold chain: maximum acceptable temperature"),
		field.JSON("special_handling", []string{}).
			Optional().
			Comment("Tags: cold_chain, hazmat, fragile, controlled_substance"),
		field.String("seal_number").
			Optional().
			Comment("Physical seal or lock ID applied at dispatch"),
		field.Time("planned_dispatch_at").
			Optional().
			Nillable(),
		field.Time("dispatched_at").
			Optional().
			Nillable(),
		field.Time("completed_at").
			Optional().
			Nillable(),
		field.String("external_reference").
			Optional().
			Comment("inventory_transfer_id or ordering reference"),
		field.JSON("metadata", map[string]any{}).
			Default(map[string]any{}).
			// Keep the DB-level DEFAULT '{}' that the original shipments migration created,
			// so Atlas does not emit a spurious DROP DEFAULT on replay (schema↔DB convergence).
			Annotations(entsql.Default("{}")),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
	}
}

// Edges of the Shipment.
func (Shipment) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("chain_of_custody", ChainOfCustody.Type),
	}
}

// Indexes of the Shipment.
func (Shipment) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "status", "created_at"),
		index.Fields("shipment_code").Unique(),
		index.Fields("external_reference"),
	}
}
