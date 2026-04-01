package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// FleetMember holds the schema definition for the FleetMember entity.
type FleetMember struct {
	ent.Schema
}

// Fields of the FleetMember.
func (FleetMember) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.UUID("fleet_id", uuid.UUID{}),
		field.UUID("user_id", uuid.UUID{}).
			Comment("References User record"),
		field.String("driver_code").
			Optional().
			Unique(),
		field.String("id_number").
			Optional().
			Comment("National ID or Passport Number"),
		field.String("license_no").
			Optional().
			Comment("Driving License Number"),
		field.String("status").
			Default("invited").
			Comment("invited, pending, pending_review, active, rejected, suspended"),
		field.String("invite_code").
			Optional().
			Unique().
			Comment("Unique invite token for email registration link"),
		field.Time("kyc_submitted_at").
			Optional().
			Nillable().
			Comment("When rider submitted KYC documents"),
		field.Time("reviewed_at").
			Optional().
			Nillable().
			Comment("When admin reviewed the application"),
		field.String("reviewed_by").
			Optional().
			Comment("Admin user ID who reviewed"),
		field.String("rejection_reason").
			Optional().
			Comment("Reason for rejection if status=rejected"),
		field.String("onboarding_source").
			Optional().
			Default("invite").
			Comment("invite | public_signup | company_created"),
		field.String("id_passport_attachment").
			Optional().
			Comment("URL to ID/Passport attachment"),
		field.String("rider_photo").
			Optional().
			Comment("URL to rider's passport photo"),
		field.UUID("vehicle_id", uuid.UUID{}).
			Optional().
			Nillable(),
		field.Time("joined_at").
			Default(time.Now),
		field.Time("suspended_at").
			Optional().
			Nillable(),
		field.JSON("metadata", map[string]any{}).
			Default(map[string]any{}),
		field.JSON("specialization_tags", []string{}).
			Default([]string{}).
			Comment("Rider capabilities: food_safe, fragile, heavy_duty, cold_chain, motorcycle, van, bicycle"),
		field.Bool("has_cold_storage").
			Default(false).
			Comment("Vehicle has temperature-controlled storage"),
		field.Float("max_weight_capacity_kg").
			Optional().
			Nillable().
			Comment("Maximum carrying weight capacity"),
		field.Float("average_rating").
			Default(0).
			Comment("Running average of rider ratings (1-5)"),
		field.Int("total_ratings").
			Default(0).
			Comment("Total number of ratings received"),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
	}
}

// Edges of the FleetMember.
func (FleetMember) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("fleet", Fleet.Type).
			Ref("members").
			Field("fleet_id").
			Unique().
			Required(),
		edge.From("user", User.Type).
			Ref("fleet_memberships").
			Field("user_id").
			Unique().
			Required(),
		edge.To("vehicle", Vehicle.Type).
			Field("vehicle_id").
			Unique(),
		edge.To("assignments", TaskAssignment.Type),
		edge.To("ratings", RiderRating.Type),
	}
}

// Indexes of the FleetMember.
func (FleetMember) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "user_id").Unique(),
		index.Fields("driver_code").Unique(),
	}
}
