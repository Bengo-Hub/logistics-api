package tasks

import (
	"entgo.io/ent/dialect/sql"
	"github.com/google/uuid"

	"github.com/bengobox/logistics-service/internal/ent/predicate"
	"github.com/bengobox/logistics-service/internal/ent/task"
)

// Metadata keys of the customer copy on a task, for the CRM link check and the CRM contact
// consumer (they are the keys withCustomerMetadata writes).
const (
	MetaCRMContactID  = metaCRMContactID
	MetaCustomerEmail = metaCustomerEmail
	MetaCustomerName  = metaCustomerName
	MetaCustomerPhone = metaCustomerPhone
)

// MetadataContact matches tasks whose metadata names the CRM contact:
// lower(metadata ->> 'crm_contact_id') = the canonical lower-case id. The metadata keeps the id as
// the source order sent it, hence lower(). The value goes through b.Arg so the driver numbers the
// placeholder (Postgres rejects a raw "?").
func MetadataContact(contactID uuid.UUID) predicate.Task {
	return predicate.Task(func(s *sql.Selector) {
		s.Where(sql.P(func(b *sql.Builder) {
			b.WriteString("lower(").Ident(s.C(task.FieldMetadata)).WriteString(" ->> '" + metaCRMContactID + "') = ").Arg(contactID.String())
		}))
	})
}
