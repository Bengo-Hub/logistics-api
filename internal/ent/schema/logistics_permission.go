package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// LogisticsPermission holds the schema definition for logistics service permissions.
type LogisticsPermission struct {
	ent.Schema
}

// Fields of the LogisticsPermission.
func (LogisticsPermission) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.String("permission_code").
			NotEmpty().
			Unique().
			Comment("Permission code: logistics.tasks.add, etc."),
		field.String("name").
			NotEmpty().
			Comment("Display name"),
		field.String("module").
			NotEmpty().
			Comment("Module: tasks, fleet, vehicles, zones, geofences, carriers, routing, telemetry, earnings, config, users"),
		field.String("action").
			NotEmpty().
			Comment("Action: add, view, view_own, change, change_own, delete, delete_own, manage, manage_own"),
		field.String("resource").
			Optional().
			Comment("Resource: tasks, fleet, vehicles, etc."),
		field.Text("description").
			Optional(),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
	}
}

// Edges of the LogisticsPermission.
func (LogisticsPermission) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("roles", LogisticsRole.Type).Ref("permissions").Through("role_permissions", RolePermission.Type),
	}
}

// Indexes of the LogisticsPermission.
func (LogisticsPermission) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("permission_code").Unique(),
		index.Fields("module"),
		index.Fields("action"),
		index.Fields("module", "action"),
	}
}
