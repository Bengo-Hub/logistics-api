package handlers

import (
	"strings"
	"time"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/google/uuid"
)

// FleetMemberResponse is the API response DTO for a fleet member.
// It flattens user identity fields from the User edge so the frontend
// receives first_name, last_name, email, phone, role as top-level fields.
type FleetMemberResponse struct {
	ID                   uuid.UUID             `json:"id"`
	TenantID             uuid.UUID             `json:"tenant_id"`
	FleetID              uuid.UUID             `json:"fleet_id"`
	UserID               uuid.UUID             `json:"user_id"`
	FirstName            string                `json:"first_name"`
	LastName             string                `json:"last_name"`
	Email                string                `json:"email"`
	Phone                string                `json:"phone"`
	Role                 string                `json:"role"`
	Status               string                `json:"status"`
	DriverCode           string                `json:"driver_code,omitempty"`
	IDPassportNumber     string                `json:"id_passport_number,omitempty"`
	IDPassportAttachment string                `json:"id_passport_attachment,omitempty"`
	RiderPhoto           string                `json:"rider_photo,omitempty"`
	AverageRating        float64               `json:"average_rating"`
	TotalRatings         int                   `json:"total_ratings"`
	SpecializationTags   []string              `json:"specialization_tags"`
	HasColdStorage       bool                  `json:"has_cold_storage"`
	MaxWeightCapacityKg  *float64              `json:"max_weight_capacity_kg,omitempty"`
	Metadata             map[string]any        `json:"metadata"`
	JoinedAt             time.Time             `json:"joined_at"`
	CreatedAt            time.Time             `json:"created_at"`
	UpdatedAt            time.Time             `json:"updated_at"`
	Edges                *FleetMemberRespEdges `json:"edges,omitempty"`
}

// FleetMemberRespEdges holds edge data for a fleet member response.
// Vehicle is wrapped in a slice to match the frontend's edges.vehicles convention.
type FleetMemberRespEdges struct {
	Vehicles []*ent.Vehicle `json:"vehicles,omitempty"`
}

// toFleetMemberResponse maps an ent.FleetMember (with loaded User + Vehicle edges)
// to the API response DTO, flattening user identity fields.
func toFleetMemberResponse(m *ent.FleetMember) *FleetMemberResponse {
	resp := &FleetMemberResponse{
		ID:                   m.ID,
		TenantID:             m.TenantID,
		FleetID:              m.FleetID,
		UserID:               m.UserID,
		Status:               m.Status,
		DriverCode:           m.DriverCode,
		IDPassportNumber:     m.IDNumber,
		IDPassportAttachment: m.IDPassportAttachment,
		RiderPhoto:           m.RiderPhoto,
		AverageRating:        m.AverageRating,
		TotalRatings:         m.TotalRatings,
		SpecializationTags:   m.SpecializationTags,
		HasColdStorage:       m.HasColdStorage,
		MaxWeightCapacityKg:  m.MaxWeightCapacityKg,
		Metadata:             m.Metadata,
		JoinedAt:             m.JoinedAt,
		CreatedAt:            m.CreatedAt,
		UpdatedAt:            m.UpdatedAt,
	}

	// Flatten User edge: email, phone, role, full_name → first_name + last_name
	if u := m.Edges.User; u != nil {
		resp.Email = u.Email
		resp.Phone = u.Phone
		resp.Role = u.Role
		if u.FullName != "" {
			parts := strings.SplitN(u.FullName, " ", 2)
			resp.FirstName = parts[0]
			if len(parts) > 1 {
				resp.LastName = parts[1]
			}
		}
	}

	// Vehicle edge: single vehicle → slice for frontend edges.vehicles compatibility
	if v := m.Edges.Vehicle; v != nil {
		resp.Edges = &FleetMemberRespEdges{
			Vehicles: []*ent.Vehicle{v},
		}
	}

	return resp
}

// toFleetMemberResponses maps a slice of ent.FleetMember to response DTOs.
func toFleetMemberResponses(members []*ent.FleetMember) []*FleetMemberResponse {
	out := make([]*FleetMemberResponse, len(members))
	for i, m := range members {
		out[i] = toFleetMemberResponse(m)
	}
	return out
}
