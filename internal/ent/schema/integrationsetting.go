package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// IntegrationSetting holds the schema definition for the IntegrationSetting entity.
type IntegrationSetting struct {
	ent.Schema
}

// Fields of the IntegrationSetting.
func (IntegrationSetting) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.String("tenant_slug").
			NotEmpty(),
		field.String("service_code").
			NotEmpty(),
		field.JSON("config_json", map[string]any{}).
			Default(map[string]any{}),
		field.String("status").
			Default("active"),
		field.Time("last_sync_at").
			Optional().
			Nillable(),
		field.JSON("metadata", map[string]any{}).
			Default(map[string]any{}),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
	}
}

// Indexes of the IntegrationSetting.
func (IntegrationSetting) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "service_code").Unique(),
		index.Fields("tenant_slug"),
	}
}
