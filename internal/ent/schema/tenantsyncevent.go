package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"github.com/google/uuid"
)

// TenantSyncEvent holds the schema definition for the TenantSyncEvent entity.
type TenantSyncEvent struct {
	ent.Schema
}

// Fields of the TenantSyncEvent.
func (TenantSyncEvent) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("tenant_id", uuid.UUID{}).
			Optional(),
		field.String("tenant_slug").
			NotEmpty(),
		field.String("source_service").
			NotEmpty().
			Comment("The service that emitted the sync event (auth-api, ordering-backend, etc.)"),
		field.JSON("payload", map[string]any{}),
		field.Time("synced_at").
			Default(time.Now),
		field.String("status").
			Default("processed"),
	}
}
