package handlers

import (
	"time"

	"github.com/bengobox/logistics-service/internal/ent"
)

// TaskResponse is the API response DTO for a task. It flattens the pickup/dropoff
// TaskStep edge (address_json + contact fields) into the top-level pickup_*/dropoff_*
// fields the rider-app and logistics-ui frontends are built against, plus the current
// assignment's rider id and timing. The raw ent.Task never exposed any of this: its
// JSON tags follow the schema's own field names (tracking_code, source_service, ...)
// and pickup/dropoff data lives entirely under a "steps" edge most callers never even
// eager-loaded, so every consumer reading task.pickup_latitude/task.dropoff_address
// etc. was silently getting undefined -- including the in-app map and the "Open in
// Google Maps" button.
type TaskResponse struct {
	ID                  string         `json:"id"`
	TenantID            string         `json:"tenant_id"`
	TenantSlug          string         `json:"tenant_slug"`
	TrackingCode        string         `json:"tracking_code,omitempty"`
	ExternalReference   string         `json:"external_reference"`
	ExternalType        string         `json:"external_type"`
	Status              string         `json:"status"`
	Priority            string         `json:"priority"`
	AssignedRiderID     *string        `json:"assigned_rider_id"`
	PickupAddress       string         `json:"pickup_address"`
	PickupLatitude      *float64       `json:"pickup_latitude"`
	PickupLongitude     *float64       `json:"pickup_longitude"`
	PickupNotes         string         `json:"pickup_notes"`
	PickupContactName   string         `json:"pickup_contact_name"`
	PickupContactPhone  string         `json:"pickup_contact_phone"`
	DropoffAddress      string         `json:"dropoff_address"`
	DropoffLatitude     *float64       `json:"dropoff_latitude"`
	DropoffLongitude    *float64       `json:"dropoff_longitude"`
	DropoffNotes        string         `json:"dropoff_notes"`
	DropoffContactName  string         `json:"dropoff_contact_name"`
	DropoffContactPhone string         `json:"dropoff_contact_phone"`
	CustomerName        string         `json:"customer_name"`
	CustomerPhone       string         `json:"customer_phone"`
	Instructions        string         `json:"instructions"`
	ItemsDescription    string         `json:"items_description"`
	ItemCount           int            `json:"item_count"`
	CashOnDelivery      float64        `json:"cash_on_delivery"`
	DistanceKm          *float64       `json:"distance_km"`
	EtaMinutes          *float64       `json:"eta_minutes"`
	EtaAt               *time.Time     `json:"eta_at"`
	AssignedAt          *time.Time     `json:"assigned_at"`
	AcceptedAt          *time.Time     `json:"accepted_at"`
	PickedUpAt          *time.Time     `json:"picked_up_at"`
	CompletedAt         *time.Time     `json:"completed_at"`
	CancelledAt         *time.Time     `json:"cancelled_at"`
	CancellationReason  string         `json:"cancellation_reason"`
	FailureReason       string         `json:"failure_reason"`
	Metadata            map[string]any `json:"metadata"`
	CreatedAt           time.Time      `json:"created_at"`
	UpdatedAt           time.Time      `json:"updated_at"`
}

// toTaskResponse maps an ent.Task (ideally with Steps and Assignments eager-loaded --
// callers that skip WithSteps()/WithAssignments() just get a response with those
// sections empty, never an error) to the flattened API response DTO.
func toTaskResponse(t *ent.Task) *TaskResponse {
	if t == nil {
		return nil
	}

	resp := &TaskResponse{
		ID:                t.ID.String(),
		TenantID:          t.TenantID.String(),
		TrackingCode:      t.TrackingCode,
		ExternalReference: t.ExternalReference,
		ExternalType:      t.TaskType,
		Status:            t.Status,
		Priority:          priorityLabel(t.Priority),
		CashOnDelivery:    t.CashOnDelivery,
		Metadata:          t.Metadata,
		CreatedAt:         t.CreatedAt,
		UpdatedAt:         t.UpdatedAt,
	}

	for _, step := range t.Edges.Steps {
		lat, lng := addressJSONCoords(step.AddressJSON)
		notes, _ := step.Metadata["instructions"].(string)

		switch step.StepType {
		case "pickup":
			resp.PickupAddress = step.LocationName
			resp.PickupLatitude = lat
			resp.PickupLongitude = lng
			resp.PickupNotes = notes
			resp.PickupContactName = step.ContactName
			resp.PickupContactPhone = step.ContactPhone
		case "dropoff":
			resp.DropoffAddress = step.LocationName
			resp.DropoffLatitude = lat
			resp.DropoffLongitude = lng
			resp.DropoffNotes = notes
			resp.DropoffContactName = step.ContactName
			resp.DropoffContactPhone = step.ContactPhone
			// The customer is the dropoff contact -- CreateTaskFromOrder stores the
			// order's customer name/phone there, not on the task itself.
			resp.CustomerName = step.ContactName
			resp.CustomerPhone = step.ContactPhone
			resp.Instructions = notes
		}
	}

	// Fall back to task-level metadata pickup coords (set as a dispatcher hint when no
	// pickup step could be created -- see CreateTaskFromOrder) if no pickup step matched.
	if resp.PickupLatitude == nil || resp.PickupLongitude == nil {
		if lat, lng := addressJSONCoords(t.Metadata); lat != nil && lng != nil {
			resp.PickupLatitude = lat
			resp.PickupLongitude = lng
		}
	}

	// Latest assignment (by assigned_at) drives assigned_rider_id/assigned_at/accepted_at.
	var latest *ent.TaskAssignment
	for _, a := range t.Edges.Assignments {
		if latest == nil || a.AssignedAt.After(latest.AssignedAt) {
			latest = a
		}
	}
	if latest != nil {
		riderID := latest.FleetMemberID.String()
		resp.AssignedRiderID = &riderID
		resp.AssignedAt = &latest.AssignedAt
		resp.AcceptedAt = latest.AcceptedAt
		resp.CompletedAt = latest.CompletedAt
	}

	return resp
}

func toTaskResponses(list []*ent.Task) []*TaskResponse {
	out := make([]*TaskResponse, 0, len(list))
	for _, t := range list {
		out = append(out, toTaskResponse(t))
	}
	return out
}

// addressJSONCoords reads "latitude"/"longitude" out of a TaskStep.AddressJSON (or a
// Task.Metadata map using the same key names) blob, tolerating the numeric types the
// JSON decoder or a direct map[string]any literal might produce.
func addressJSONCoords(m map[string]any) (*float64, *float64) {
	lat, latOK := toFloat64(m["latitude"])
	lng, lngOK := toFloat64(m["longitude"])
	if !latOK || !lngOK || (lat == 0 && lng == 0) {
		return nil, nil
	}
	return &lat, &lng
}

func toFloat64(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

// priorityLabel maps the ent Task.priority int column to the label the frontend's
// TaskPriority enum expects. No task-creation path in this codebase currently sets a
// non-zero priority, so this is a best-effort mapping rather than a confirmed scale.
func priorityLabel(p int) string {
	switch {
	case p <= 0:
		return "normal"
	case p == 1:
		return "high"
	default:
		return "urgent"
	}
}
