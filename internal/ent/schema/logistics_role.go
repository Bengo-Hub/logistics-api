package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// LogisticsRole holds the schema definition for logistics service roles. Roles are platform
// reference data (no tenant_id); UserRoleAssignment carries the tenant.
type LogisticsRole struct {
	ent.Schema
}

// Fields of the LogisticsRole.
func (LogisticsRole) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.String("role_code").
			NotEmpty().
			Comment("Role code (rbac.SystemRoles: admin, dispatcher, driver). Platform-wide: the same role grants the same permissions in every tenant"),
		field.String("name").
			NotEmpty().
			Comment("Display name"),
		field.Text("description").
			Optional(),
		field.Bool("is_system_role").
			Default(false).
			Comment("System roles cannot be deleted"),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
	}
}

// Edges of the LogisticsRole.
func (LogisticsRole) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("permissions", LogisticsPermission.Type).Through("role_permissions", RolePermission.Type),
		// The inverse of UserRoleAssignment.role (its role_id column). A plain edge.To here
		// made Ent add a second, never-set FK column, so assigned roles granted nothing.
		edge.From("user_assignments", UserRoleAssignment.Type).Ref("role"),
	}
}

// Indexes of the LogisticsRole.
func (LogisticsRole) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("role_code").Unique(),
		index.Fields("is_system_role"),
	}
}
