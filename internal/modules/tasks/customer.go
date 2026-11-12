package tasks

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/platform/events"
)

// Customer identity on a task. Ordering owns the order and its customer, and the CRM (marketflow)
// owns customer identity, so logistics keeps no customer table. What the source order supplied
// rides in the task's existing metadata under these keys and goes out on every task event
// (withCustomer), so notifications-api can write to the customer. The rider reads the customer's
// name and phone from the drop-off step; the email and CRM id are not returned by the task API
// (handlers.secretTaskMetadataKeys).
const (
	metaCRMContactID  = "crm_contact_id"
	metaCustomerEmail = "customer_email"
	metaCustomerName  = "customer_name"
	metaCustomerPhone = "customer_phone"
)

// customerIdentity is the customer a source order names.
type customerIdentity struct {
	CRMContactID string
	Email        string
	Name         string
	Phone        string
}

// withCustomerMetadata adds the customer's identity to task metadata. Values already in the
// metadata win (a caller that put them there knows best); blank values are skipped. A
// crm_contact_id that is not a UUID is skipped too: it could never be looked up, and because a
// crm_contact_id makes the task events drop the email (TaskEventData.toMap), keeping it would
// leave the customer unreachable. It returns meta, or a new map when meta was nil and something
// was added.
func withCustomerMetadata(meta map[string]any, c customerIdentity) map[string]any {
	if _, err := uuid.Parse(strings.TrimSpace(c.CRMContactID)); err != nil {
		c.CRMContactID = ""
	}
	for _, kv := range [][2]string{
		{metaCRMContactID, c.CRMContactID},
		{metaCustomerEmail, c.Email},
		{metaCustomerName, c.Name},
		{metaCustomerPhone, c.Phone},
	} {
		v := strings.TrimSpace(kv[1])
		if v == "" || metadataString(meta, kv[0]) != "" {
			continue
		}
		if meta == nil {
			meta = map[string]any{}
		}
		meta[kv[0]] = v
	}
	return meta
}

// withCustomer puts the task's customer identity and due time on a task event. The drop-off
// step's contact (when the steps are loaded) fills a name or phone the metadata lacks, which
// covers tasks created before the customer was kept in the metadata.
func withCustomer(d events.TaskEventData, t *ent.Task) events.TaskEventData {
	if t == nil {
		return d
	}
	// Only a valid id: a caller's own metadata could carry anything, and an invalid id would make
	// the event drop the email and notifications send nothing.
	if id, err := uuid.Parse(strings.TrimSpace(metadataString(t.Metadata, metaCRMContactID))); err == nil && id != uuid.Nil {
		d.CRMContactID = id.String()
	}
	d.CustomerEmail = metadataString(t.Metadata, metaCustomerEmail)
	d.CustomerName = metadataString(t.Metadata, metaCustomerName)
	d.CustomerPhone = metadataString(t.Metadata, metaCustomerPhone)
	for _, st := range t.Edges.Steps {
		if st == nil || st.StepType != "dropoff" {
			continue
		}
		if d.CustomerName == "" {
			d.CustomerName = strings.TrimSpace(st.ContactName)
		}
		if d.CustomerPhone == "" {
			d.CustomerPhone = strings.TrimSpace(st.ContactPhone)
		}
		break
	}
	if d.EstimatedArrival == "" {
		d.EstimatedArrival = estimatedArrival(t, time.Now())
	}
	return d
}

// estimatedArrival is when the order is due at the customer: the requested drop-off time, else
// the SLA due time. Only a time still after now counts: a due time that has passed would tell
// the customer the order should already be there, so it is skipped ("" when no time is left).
// The live routing ETA is not used here: it is computed after the status change and, before
// pickup, measures the trip to the outlet.
func estimatedArrival(t *ent.Task, now time.Time) string {
	for _, at := range []*time.Time{t.RequestedDropoffAt, t.SLADueAt} {
		if at != nil && !at.IsZero() && at.After(now) {
			return at.UTC().Format(time.RFC3339)
		}
	}
	return ""
}
