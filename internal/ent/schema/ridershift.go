package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// RiderShift holds the schema definition for the RiderShift entity.
type RiderShift struct {
	ent.Schema
}

// Fields of the RiderShift.
func (RiderShift) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New).Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.UUID("fleet_member_id", uuid.UUID{}).Comment("FK to FleetMember"),
		field.Time("shift_start"),
		field.Time("shift_end"),
		field.Enum("status").Values("scheduled", "active", "completed", "cancelled").Default("scheduled"),
		field.JSON("zone_ids", []uuid.UUID{}).Optional().Comment("Geofence zones rider covers during this shift"),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

// Indexes of the RiderShift.
func (RiderShift) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "fleet_member_id", "shift_start"),
		index.Fields("status"),
	}
}
