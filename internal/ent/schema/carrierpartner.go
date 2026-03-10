package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"github.com/google/uuid"
)

// CarrierPartner holds the schema definition for the CarrierPartner entity.
type CarrierPartner struct {
	ent.Schema
}

// Fields of the CarrierPartner.
func (CarrierPartner) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.String("name").
			NotEmpty(),
		field.String("provider_code").
			NotEmpty(),
		field.JSON("api_credentials_json", map[string]any{}).
			Optional(),
		field.String("status").
			Default("active"),
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
