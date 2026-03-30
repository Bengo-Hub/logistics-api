package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// RiderRating holds the schema definition for the RiderRating entity.
type RiderRating struct {
	ent.Schema
}

// Fields of the RiderRating.
func (RiderRating) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.UUID("fleet_member_id", uuid.UUID{}).
			Comment("Rider being rated"),
		field.UUID("task_id", uuid.UUID{}).
			Optional().
			Nillable().
			Comment("Delivery task associated with this rating"),
		field.String("order_id").
			Optional().
			Comment("External order reference from ordering-service"),
		field.String("customer_user_id").
			Optional().
			Comment("User ID of the customer who submitted the rating"),
		field.Int("rating").
			Min(1).
			Max(5).
			Comment("Star rating 1-5"),
		field.Text("comment").
			Optional().
			Comment("Customer feedback text"),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
	}
}

// Edges of the RiderRating.
func (RiderRating) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("fleet_member", FleetMember.Type).
			Ref("ratings").
			Field("fleet_member_id").
			Unique().
			Required(),
	}
}

// Indexes of the RiderRating.
func (RiderRating) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "fleet_member_id"),
		index.Fields("task_id"),
		index.Fields("order_id"),
	}
}
