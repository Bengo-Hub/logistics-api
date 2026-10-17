package fleet

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/fleetmember"
	entvehicle "github.com/bengobox/logistics-service/internal/ent/vehicle"
)

// Errors the handlers map to 409 so the admin sees a precise message.
var (
	ErrAlreadyRider  = errors.New("fleet: this person is already a rider in your fleet")
	ErrVehicleTaken  = errors.New("fleet: that vehicle is already assigned to another rider")
	ErrPlateInUse    = errors.New("fleet: a vehicle with this number plate already exists")
	ErrVehicleAbsent = errors.New("fleet: vehicle not found")
)

// MetaVehicleOwner marks a vehicle the rider registered on their own profile. Those go with the
// rider when the member is deleted; vehicles the business owns are fleet assets and stay.
const (
	MetaVehicleOwner  = "owner"
	VehicleOwnerRider = "rider"
)

// vehicleTypeAliases maps every spelling in use (rider-app said "bike", logistics-ui says
// "motorcycle") to the one stored value.
var vehicleTypeAliases = map[string]string{
	"motorcycle": "motorcycle",
	"motorbike":  "motorcycle",
	"motor_bike": "motorcycle",
	"motor bike": "motorcycle",
	"bike":       "motorcycle",
	"boda":       "motorcycle",
	"bicycle":    "bicycle",
	"car":        "car",
	"van":        "van",
	"truck":      "truck",
	"other":      "other",
}

// NormalizeVehicleType returns the canonical vehicle type or an error for an unknown one.
func NormalizeVehicleType(t string) (string, error) {
	key := strings.ToLower(strings.TrimSpace(t))
	if v, ok := vehicleTypeAliases[key]; ok {
		return v, nil
	}
	return "", fmt.Errorf("fleet: unknown vehicle type %q", t)
}

// NormalizePlate upper-cases a number plate and collapses its spaces so "kmfa 123x" and
// "KMFA 123X" are the same vehicle.
func NormalizePlate(p string) string {
	return strings.ToUpper(strings.Join(strings.Fields(p), " "))
}

// CreateVehicleRecord is the one place a vehicle row is built. vc is the client or a
// transaction's client. Make and model default to "Unknown" because the schema requires them
// and a rider's own bike often has neither on paper.
func CreateVehicleRecord(ctx context.Context, vc *ent.VehicleClient, tenantID, fleetID uuid.UUID, req CreateVehicleRequest, meta map[string]any) (*ent.Vehicle, error) {
	vt, err := NormalizeVehicleType(req.VehicleType)
	if err != nil {
		return nil, err
	}
	plate := NormalizePlate(req.LicensePlate)
	if plate == "" {
		return nil, fmt.Errorf("fleet: number plate is required")
	}
	makeName, model := strings.TrimSpace(req.Make), strings.TrimSpace(req.Model)
	if makeName == "" {
		makeName = "Unknown"
	}
	if model == "" {
		model = "Unknown"
	}
	b := vc.Create().
		SetTenantID(tenantID).
		SetFleetID(fleetID).
		SetVehicleType(vt).
		SetMake(makeName).
		SetModel(model).
		SetLicensePlate(plate).
		SetStatus("active").
		SetComplianceStatus("pending")
	if len(meta) > 0 {
		b.SetMetadata(meta)
	}
	v, err := b.Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, ErrPlateInUse
		}
		return nil, fmt.Errorf("fleet: create vehicle: %w", err)
	}
	return v, nil
}

// requireFreeVehicle checks the vehicle belongs to the tenant and is not another rider's.
// exceptMember is the rider being linked (their own link does not count).
func requireFreeVehicle(ctx context.Context, c *ent.Client, tx *ent.Tx, tenantID, vehicleID, exceptMember uuid.UUID) error {
	vq, mq := c.Vehicle, c.FleetMember
	if tx != nil {
		vq, mq = tx.Vehicle, tx.FleetMember
	}
	ok, err := vq.Query().Where(entvehicle.ID(vehicleID), entvehicle.TenantID(tenantID)).Exist(ctx)
	if err != nil {
		return fmt.Errorf("fleet: check vehicle: %w", err)
	}
	if !ok {
		return ErrVehicleAbsent
	}
	q := mq.Query().Where(fleetmember.TenantID(tenantID), fleetmember.VehicleID(vehicleID))
	if exceptMember != uuid.Nil {
		q = q.Where(fleetmember.IDNEQ(exceptMember))
	}
	taken, err := q.Exist(ctx)
	if err != nil {
		return fmt.Errorf("fleet: check vehicle link: %w", err)
	}
	if taken {
		return ErrVehicleTaken
	}
	return nil
}
