package fleet

import (
	"context"
	"errors"
	"strings"

	"github.com/Bengo-Hub/httpware/contact"
	"github.com/google/uuid"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/fleetmember"
)

// ErrInvalidMemberDetails marks input the caller got wrong (400).
var ErrInvalidMemberDetails = errors.New("fleet: invalid rider details")

// MemberDetails are the rider details a fleet manager may correct: the phone customers and
// dispatchers call, and the ID and licence numbers. Nil fields are left as they are. Name and
// email belong to the rider's account (auth-api) and are not edited here.
type MemberDetails struct {
	Phone     *string `json:"phone,omitempty"`
	IDNumber  *string `json:"id_number,omitempty"`
	LicenseNo *string `json:"license_no,omitempty"`
}

// normalize checks the details and returns the phone in E.164 (Kenyan local numbers allowed).
func (d MemberDetails) normalize() (MemberDetails, error) {
	out := d
	if d.Phone != nil {
		raw := strings.TrimSpace(*d.Phone)
		if raw == "" {
			return d, errors.Join(ErrInvalidMemberDetails, errors.New("enter the rider's phone number"))
		}
		e164, err := contact.NormalizePhone(raw, "KE")
		if err != nil {
			return d, errors.Join(ErrInvalidMemberDetails, errors.New("enter a valid phone number, for example 0712 345 678"))
		}
		out.Phone = &e164
	}
	if d.IDNumber != nil {
		v := strings.TrimSpace(*d.IDNumber)
		if v == "" {
			return d, errors.Join(ErrInvalidMemberDetails, errors.New("the ID number cannot be empty"))
		}
		out.IDNumber = &v
	}
	if d.LicenseNo != nil {
		v := strings.TrimSpace(*d.LicenseNo)
		out.LicenseNo = &v
	}
	return out, nil
}

// UpdateMemberDetails corrects a rider's details as a fleet manager. The manager is vouching for
// the change, so unlike the rider's own profile save it never sends the rider back to review.
func (s *Service) UpdateMemberDetails(ctx context.Context, tenantID, memberID uuid.UUID, in MemberDetails) (*ent.FleetMember, error) {
	d, err := in.normalize()
	if err != nil {
		return nil, err
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	m, err := tx.FleetMember.Query().Where(fleetmember.ID(memberID), fleetmember.TenantID(tenantID)).Only(ctx)
	if err != nil {
		return nil, err
	}
	if d.Phone != nil {
		if err := tx.User.UpdateOneID(m.UserID).SetPhone(*d.Phone).Exec(ctx); err != nil {
			return nil, err
		}
	}
	upd := tx.FleetMember.UpdateOne(m)
	if d.IDNumber != nil {
		upd.SetIDNumber(*d.IDNumber)
	}
	if d.LicenseNo != nil {
		upd.SetLicenseNo(*d.LicenseNo)
	}
	saved, err := upd.Save(ctx)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return saved, nil
}
