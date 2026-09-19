package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// LogisticsNotification holds the schema definition for the LogisticsNotification entity.
// Tenant-wide operational alerts for dispatchers/admins (SLA breaches, auto-dispatch
// failures needing manual attention) surfaced by logistics-ui's notification bell — not
// per-user, since these are team-visible operational signals rather than personal messages
// (any dispatcher acknowledging one dismisses it for the whole tenant, matching how a
// shared ops alert is normally handled).
type LogisticsNotification struct {
	ent.Schema
}

// Fields of the LogisticsNotification.
func (LogisticsNotification) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.String("notification_type").
			NotEmpty().
			Comment("e.g. sla_breach, dispatch_failed"),
		field.String("title").
			NotEmpty(),
		field.Text("body").
			Optional(),
		field.JSON("payload", map[string]any{}).
			Default(map[string]any{}),
		field.UUID("related_task_id", uuid.UUID{}).
			Optional().
			Nillable(),
		field.Bool("is_read").
			Default(false),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
	}
}

// Indexes of the LogisticsNotification.
func (LogisticsNotification) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "is_read", "created_at"),
		index.Fields("tenant_id", "notification_type", "related_task_id"),
	}
}
