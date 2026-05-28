package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// Outlet mirrors a logistics-use-case outlet from auth-api.
// Only outlets with use_case="logistics" are synced here via auth.outlet.* JetStream events.
// The id matches auth-api's deterministic UUID so cross-service lookups are zero-ambiguity.
type Outlet struct {
	ent.Schema
}

// Fields of the Outlet.
func (Outlet) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),

		field.UUID("tenant_id", uuid.UUID{}).
			Comment("Owning tenant"),

		field.String("code").
			NotEmpty().
			Comment("Short identifier unique per tenant, matches auth-api outlet code"),

		field.String("name").
			NotEmpty().
			Comment("Display name of the logistics hub"),

		field.String("use_case").
			Default("logistics").
			Comment("Always 'logistics' for outlets synced to this service"),

		field.String("address").
			Optional().
			Nillable(),

		field.Float("latitude").
			Optional().
			Nillable().
			Comment("GPS latitude for routing and geofencing"),

		field.Float("longitude").
			Optional().
			Nillable().
			Comment("GPS longitude for routing and geofencing"),

		field.String("timezone").
			Optional().
			Nillable().
			Default("Africa/Nairobi").
			Comment("IANA timezone for shift boundaries"),

		field.Bool("is_hq").
			Default(false).
			Comment("HQ outlets can view data across all sibling outlets"),

		field.String("status").
			Default("active").
			Comment("active | closed | archived"),

		field.Time("created_at").
			Default(time.Now).
			Immutable(),

		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
	}
}

// Edges of the Outlet.
func (Outlet) Edges() []ent.Edge {
	return nil
}

// Indexes of the Outlet.
func (Outlet) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "code").Unique(),
		index.Fields("tenant_id", "status"),
	}
}
