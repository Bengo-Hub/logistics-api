package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/bengobox/logistics-service/internal/ent"
	fleetmod "github.com/bengobox/logistics-service/internal/modules/fleet"
)

// ErrInvalidProfile marks a profile save the rider got wrong (missing ID number, unknown vehicle
// type); the handler answers 400 instead of 500.
var ErrInvalidProfile = errors.New("invalid profile")

// saveRiderVehicle applies the vehicle part of a rider's profile save inside tx. Blank fields
// are left as they are, so the vehicle an admin linked is not wiped. Staff riders skip the
// compliance reset: HR vetted them and there are no vehicle documents to review.
func (s *Service) saveRiderVehicle(ctx context.Context, tx *ent.Tx, fm *ent.FleetMember, staff bool, req UpdateRiderProfileRequest) error {
	vehicleType := strings.TrimSpace(req.VehicleType)
	plate := fleetmod.NormalizePlate(req.LicensePlate)

	if fm.VehicleID != nil {
		upd := tx.Vehicle.UpdateOneID(*fm.VehicleID)
		if vehicleType != "" {
			vt, err := fleetmod.NormalizeVehicleType(vehicleType)
			if err != nil {
				return fmt.Errorf("%w: %v", ErrInvalidProfile, err)
			}
			upd.SetVehicleType(vt)
		}
		if plate != "" {
			upd.SetLicensePlate(plate)
		}
		if req.ImageLicensePlate != "" {
			upd.SetImageLicensePlate(req.ImageLicensePlate)
		}
		if req.ImageSideView != "" {
			upd.SetImageSideView(req.ImageSideView)
		}
		if !staff {
			upd.SetComplianceStatus("pending")
		}
		if err := upd.Exec(ctx); err != nil {
			if ent.IsConstraintError(err) {
				return fleetmod.ErrPlateInUse
			}
			return fmt.Errorf("update vehicle: %w", err)
		}
		return nil
	}

	// No vehicle yet: the plate is what makes one, so a save without it registers nothing.
	if plate == "" {
		return nil
	}
	if vehicleType == "" {
		return fmt.Errorf("%w: select a vehicle type", ErrInvalidProfile)
	}
	v, err := fleetmod.CreateVehicleRecord(ctx, tx.Vehicle, fm.TenantID, fm.FleetID, fleetmod.CreateVehicleRequest{
		VehicleType:  vehicleType,
		LicensePlate: plate,
	}, map[string]any{fleetmod.MetaVehicleOwner: fleetmod.VehicleOwnerRider})
	if err != nil {
		if errors.Is(err, fleetmod.ErrPlateInUse) {
			return err
		}
		return fmt.Errorf("%w: %v", ErrInvalidProfile, err)
	}
	upd := tx.Vehicle.UpdateOne(v)
	if req.ImageLicensePlate != "" {
		upd.SetImageLicensePlate(req.ImageLicensePlate)
	}
	if req.ImageSideView != "" {
		upd.SetImageSideView(req.ImageSideView)
	}
	if err := upd.Exec(ctx); err != nil {
		return fmt.Errorf("update vehicle: %w", err)
	}
	if err := tx.FleetMember.UpdateOne(fm).SetVehicleID(v.ID).Exec(ctx); err != nil {
		return fmt.Errorf("link vehicle: %w", err)
	}
	return nil
}

// InsurancePolicy returns the tenant's vehicle insurance rules (fleet.LoadInsurancePolicy).
func (s *Service) InsurancePolicy(ctx context.Context, tenantID uuid.UUID) fleetmod.InsurancePolicy {
	return fleetmod.LoadInsurancePolicy(ctx, s.client, tenantID)
}

// rekey moves a stub user onto its auth id in its own transaction (fleet.RekeyUser).
func (s *Service) rekey(ctx context.Context, stub *ent.User, authID uuid.UUID, f fleetmod.RekeyFields) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := fleetmod.RekeyUser(ctx, tx, stub, authID, f); err != nil {
		return err
	}
	return tx.Commit()
}
