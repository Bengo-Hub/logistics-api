package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// LogisticsNotification holds the schema definition for the LogisticsNotification entity.
// Two audiences in one table:
//   - recipient_member_id NULL: tenant-wide operational alerts for dispatchers (SLA breaches,
//     auto-dispatch failures) on logistics-ui's bell. Team-visible, so any dispatcher reading
//     one dismisses it for the tenant.
//   - recipient_member_id set: a rider's own inbox in the rider app (job assigned, cancelled or
//     moved, payout sent). Only that rider sees or reads it.
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
		field.UUID("recipient_member_id", uuid.UUID{}).
			Optional().
			Nillable().
			Comment("Rider (fleet member) the message is for; NULL = dispatcher alert for the tenant"),
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
		// A rider's inbox: newest first, unread count.
		index.Fields("tenant_id", "recipient_member_id", "created_at"),
	}
}
