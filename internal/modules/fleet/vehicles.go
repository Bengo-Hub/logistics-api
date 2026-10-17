package fleet

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/fleetmember"
	entvehicle "github.com/bengobox/logistics-service/internal/ent/vehicle"
)

// ListVehiclesParams filters the fleet's vehicle list. Assigned nil means both.
type ListVehiclesParams struct {
	Search   string
	Status   string
	Assigned *bool
	Limit    int
	Offset   int
}

// VehicleSummary counts the tenant's whole fleet (not just the page) in SQL.
type VehicleSummary struct {
	Active      int `json:"active"`
	Maintenance int `json:"maintenance"`
	Inactive    int `json:"inactive"`
	Assigned    int `json:"assigned"`
	Unassigned  int `json:"unassigned"`
}

// ListVehicles returns one page of the tenant's vehicles with the rider each is linked to, and
// the fleet-wide summary. Vehicles with no rider are included: they are what an admin links to
// a new rider.
func (s *Service) ListVehicles(ctx context.Context, tenantID uuid.UUID, p ListVehiclesParams) ([]*ent.Vehicle, int, VehicleSummary, error) {
	q := s.client.Vehicle.Query().Where(entvehicle.TenantID(tenantID))
	if p.Status != "" {
		q = q.Where(entvehicle.Status(p.Status))
	}
	if term := strings.TrimSpace(p.Search); term != "" {
		q = q.Where(entvehicle.Or(
			entvehicle.LicensePlateContainsFold(term),
			entvehicle.MakeContainsFold(term),
			entvehicle.ModelContainsFold(term),
		))
	}
	if p.Assigned != nil {
		if *p.Assigned {
			q = q.Where(entvehicle.HasMembers())
		} else {
			q = q.Where(entvehicle.Not(entvehicle.HasMembers()))
		}
	}
	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, 0, VehicleSummary{}, fmt.Errorf("fleet: count vehicles: %w", err)
	}
	vehicles, err := q.
		WithMembers(func(mq *ent.FleetMemberQuery) {
			mq.Where(fleetmember.TenantID(tenantID)).WithUser()
		}).
		Order(ent.Desc(entvehicle.FieldCreatedAt), ent.Asc(entvehicle.FieldID)).
		Limit(p.Limit).
		Offset(p.Offset).
		All(ctx)
	if err != nil {
		return nil, 0, VehicleSummary{}, fmt.Errorf("fleet: list vehicles: %w", err)
	}
	sum, err := s.vehicleSummary(ctx, tenantID)
	if err != nil {
		return nil, 0, VehicleSummary{}, err
	}
	return vehicles, total, sum, nil
}

func (s *Service) vehicleSummary(ctx context.Context, tenantID uuid.UUID) (VehicleSummary, error) {
	var rows []struct {
		Status string `json:"status"`
		Count  int    `json:"count"`
	}
	if err := s.client.Vehicle.Query().
		Where(entvehicle.TenantID(tenantID)).
		GroupBy(entvehicle.FieldStatus).
		Aggregate(ent.Count()).
		Scan(ctx, &rows); err != nil {
		return VehicleSummary{}, fmt.Errorf("fleet: vehicle summary: %w", err)
	}
	var sum VehicleSummary
	total := 0
	for _, r := range rows {
		total += r.Count
		switch r.Status {
		case "active":
			sum.Active = r.Count
		case "maintenance":
			sum.Maintenance = r.Count
		case "inactive":
			sum.Inactive = r.Count
		}
	}
	assigned, err := s.client.Vehicle.Query().
		Where(entvehicle.TenantID(tenantID), entvehicle.HasMembers()).
		Count(ctx)
	if err != nil {
		return VehicleSummary{}, fmt.Errorf("fleet: vehicle summary: %w", err)
	}
	sum.Assigned, sum.Unassigned = assigned, total-assigned
	return sum, nil
}
