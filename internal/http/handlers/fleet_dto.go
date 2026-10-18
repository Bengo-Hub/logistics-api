package handlers

import (
	"strings"
	"time"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/modules/fleet"
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
	// VehicleInsurance and DispatchBlock come from withJobEligibility (list and detail).
	VehicleInsurance     *fleet.InsuranceView `json:"vehicle_insurance,omitempty"`
	DispatchBlock        string               `json:"dispatch_block,omitempty"`
	DispatchBlockMessage string               `json:"dispatch_block_message,omitempty"`
}

// FleetMemberRespEdges holds edge data for a fleet member response.
// Vehicle is wrapped in a slice to match the frontend's edges.vehicles convention.
type FleetMemberRespEdges struct {
	Vehicles []*ent.Vehicle `json:"vehicles,omitempty"`
}

// toFleetMemberResponse maps an ent.FleetMember (with loaded User + Vehicle edges)
// to the API response DTO, flattening user identity fields.
func toFleetMemberResponse(m *ent.FleetMember) *FleetMemberResponse {
	signFleetMemberMedia(m)
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

// VehicleAssignee is the rider a vehicle is linked to.
type VehicleAssignee struct {
	MemberID uuid.UUID `json:"member_id"`
	Name     string    `json:"name"`
	Phone    string    `json:"phone,omitempty"`
}

// VehicleResponse is a vehicle with the rider it is linked to (nil when it is free) and its
// insurance status under the tenant's policy.
type VehicleResponse struct {
	*ent.Vehicle
	AssignedTo *VehicleAssignee     `json:"assigned_to,omitempty"`
	ChassisNo  string               `json:"chassis_no,omitempty"`
	Insurance  *fleet.InsuranceView `json:"insurance,omitempty"`
}

// toVehicleResponse signs the vehicle's private photos and documents and adds its insurance view.
func toVehicleResponse(v *ent.Vehicle, p fleet.InsurancePolicy, now time.Time) *VehicleResponse {
	r := &VehicleResponse{Vehicle: signVehicleMedia(v), Insurance: fleet.InsuranceStatusOf(v, p, now)}
	r.ChassisNo, _ = v.Metadata[fleet.MetaChassisNo].(string)
	return r
}

// toVehicleResponses maps vehicles loaded with their members (and users) to response DTOs.
func toVehicleResponses(vehicles []*ent.Vehicle, p fleet.InsurancePolicy) []*VehicleResponse {
	now := time.Now()
	out := make([]*VehicleResponse, len(vehicles))
	for i, v := range vehicles {
		r := toVehicleResponse(v, p, now)
		if len(v.Edges.Members) > 0 {
			m := v.Edges.Members[0]
			a := &VehicleAssignee{MemberID: m.ID}
			if u := m.Edges.User; u != nil {
				a.Name, a.Phone = u.FullName, u.Phone
			}
			r.AssignedTo = a
		}
		out[i] = r
	}
	return out
}

// toFleetMemberResponses maps a slice of ent.FleetMember to response DTOs.
func toFleetMemberResponses(members []*ent.FleetMember) []*FleetMemberResponse {
	out := make([]*FleetMemberResponse, len(members))
	for i, m := range members {
		out[i] = toFleetMemberResponse(m)
	}
	return out
}

// withJobEligibility adds the vehicle's insurance view and why the rider gets no jobs, if so
// (same rule as dispatch: fleet.VehicleBlock), to responses built from members.
func withJobEligibility(resps []*FleetMemberResponse, members []*ent.FleetMember, p fleet.InsurancePolicy) {
	now := time.Now()
	for i, m := range members {
		v := m.Edges.Vehicle
		if v == nil {
			continue
		}
		// toFleetMemberResponse already signed the vehicle's media, the document included.
		view := fleet.InsuranceStatusOf(v, p, now)
		resps[i].VehicleInsurance = view
		if m.Status == "active" && view.Block != "" {
			resps[i].DispatchBlock = view.Block
			resps[i].DispatchBlockMessage = fleet.BlockMessage(view.Block, v.LicensePlate)
		}
	}
}
