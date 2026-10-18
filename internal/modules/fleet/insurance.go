package fleet

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/fleetmember"
	"github.com/bengobox/logistics-service/internal/ent/predicate"
	entvehicle "github.com/bengobox/logistics-service/internal/ent/vehicle"
	"github.com/bengobox/logistics-service/internal/modules/settings"
	"github.com/bengobox/logistics-service/internal/shared/localtime"
)

// Vehicle insurance. The expiry is the vehicles.insurance_expiry column (indexed for the monitor)
// and the certificate the insurance_document column; the policy details live in
// vehicles.metadata["insurance"]. Whether a missing or expired policy takes a rider off jobs is a
// per-tenant setting with a platform default (both default to block).
const (
	ConfigBlockUninsured        = "logistics.block_uninsured_vehicles"
	ConfigBlockExpiredInsurance = "logistics.block_expired_insurance"
	ConfigInsuranceReminderDays = "logistics.insurance_reminder_days"

	defaultReminderDays = 7
	maxReminderDays     = 60

	metaInsurance        = "insurance"
	metaInsuranceHistory = "insurance_history"
	metaInactiveReason   = "inactive_reason"
	// InactiveInsuranceExpired marks a vehicle the monitor deactivated; recording a valid policy
	// reactivates it. A vehicle an admin set inactive by hand stays inactive.
	InactiveInsuranceExpired = "insurance_expired"
	insuranceHistoryLimit    = 5
)

// Reasons a rider gets no jobs because of their vehicle (empty = allowed).
const (
	BlockVehicleInactive  = "vehicle_inactive"
	BlockNoInsurance      = "no_insurance"
	BlockInsuranceExpired = "insurance_expired"
)

// NeedsInsurance reports whether a vehicle type must be insured: anything motorised. Bicycles
// never need it.
func NeedsInsurance(vehicleType string) bool { return vehicleType != "bicycle" }

// InsurancePolicy is a tenant's insurance rules.
type InsurancePolicy struct {
	BlockUninsured bool `json:"block_uninsured"`
	BlockExpired   bool `json:"block_expired"`
	ReminderDays   int  `json:"reminder_days"`
}

// LoadInsurancePolicy reads the tenant's rules (tenant value, else platform default, else block).
func LoadInsurancePolicy(ctx context.Context, client *ent.Client, tenantID uuid.UUID) InsurancePolicy {
	return InsurancePolicy{
		BlockUninsured: settings.Bool(ctx, client, tenantID, ConfigBlockUninsured, true),
		BlockExpired:   settings.Bool(ctx, client, tenantID, ConfigBlockExpiredInsurance, true),
		ReminderDays:   settings.Int(ctx, client, tenantID, ConfigInsuranceReminderDays, defaultReminderDays, 1, maxReminderDays),
	}
}

// InsurancePolicy returns the tenant's insurance rules.
func (s *Service) InsurancePolicy(ctx context.Context, tenantID uuid.UUID) InsurancePolicy {
	return LoadInsurancePolicy(ctx, s.client, tenantID)
}

// Insurance is the policy recorded on a vehicle (metadata["insurance"]).
type Insurance struct {
	PolicyNo      string `json:"policy_no"`
	CertificateNo string `json:"certificate_no,omitempty"`
	Insurer       string `json:"insurer,omitempty"`
	Cover         string `json:"cover,omitempty"`
	StartsOn      string `json:"starts_on,omitempty"`
	UpdatedBy     string `json:"updated_by,omitempty"`
	UpdatedAt     string `json:"updated_at,omitempty"`
	// RemindedFor and ExpiredNoticeFor hold the expiry date a notice went out for, so each
	// policy gets one reminder and one expiry notice.
	RemindedFor      string `json:"reminded_for,omitempty"`
	ExpiredNoticeFor string `json:"expired_notice_for,omitempty"`
}

// InsuranceOf reads a vehicle's recorded policy.
func InsuranceOf(v *ent.Vehicle) Insurance {
	var in Insurance
	if v == nil || v.Metadata == nil {
		return in
	}
	raw, ok := v.Metadata[metaInsurance].(map[string]any)
	if !ok {
		return in
	}
	str := func(k string) string { s, _ := raw[k].(string); return s }
	in.PolicyNo, in.CertificateNo, in.Insurer, in.Cover = str("policy_no"), str("certificate_no"), str("insurer"), str("cover")
	in.StartsOn, in.UpdatedBy, in.UpdatedAt = str("starts_on"), str("updated_by"), str("updated_at")
	in.RemindedFor, in.ExpiredNoticeFor = str("reminded_for"), str("expired_notice_for")
	return in
}

func (in Insurance) toMap() map[string]any {
	m := map[string]any{"policy_no": in.PolicyNo}
	for k, v := range map[string]string{
		"certificate_no": in.CertificateNo, "insurer": in.Insurer, "cover": in.Cover, "starts_on": in.StartsOn,
		"updated_by": in.UpdatedBy, "updated_at": in.UpdatedAt, "reminded_for": in.RemindedFor,
		"expired_notice_for": in.ExpiredNoticeFor,
	} {
		if v != "" {
			m[k] = v
		}
	}
	return m
}

// Insurance statuses shown to people.
const (
	InsuranceValid       = "valid"
	InsuranceExpiring    = "expiring"
	InsuranceExpired     = "expired"
	InsuranceMissing     = "missing"
	InsuranceNotRequired = "not_required"
)

// InsuranceView is a vehicle's policy with its status under the tenant's policy, for the UIs.
type InsuranceView struct {
	Insurance
	ExpiresOn string `json:"expires_on,omitempty"`
	Document  string `json:"document,omitempty"`
	Status    string `json:"status"`
	DaysLeft  *int   `json:"days_left,omitempty"`
	// Block is why the vehicle may not carry jobs now ("" = it may).
	Block string `json:"block,omitempty"`
}

// InsuranceStatusOf describes v's insurance at now under policy p.
func InsuranceStatusOf(v *ent.Vehicle, p InsurancePolicy, now time.Time) *InsuranceView {
	if v == nil {
		return nil
	}
	view := &InsuranceView{Insurance: InsuranceOf(v), Document: v.InsuranceDocument, Block: VehicleBlock(v, p, now)}
	// Internal markers are not for display.
	view.RemindedFor, view.ExpiredNoticeFor = "", ""
	switch {
	case v.InsuranceExpiry == nil && !NeedsInsurance(v.VehicleType):
		view.Status = InsuranceNotRequired
	case v.InsuranceExpiry == nil:
		view.Status = InsuranceMissing
	default:
		view.ExpiresOn = localtime.Date(*v.InsuranceExpiry)
		days := localtime.DaysUntil(now, *v.InsuranceExpiry)
		view.DaysLeft = &days
		switch {
		case !v.InsuranceExpiry.After(now):
			view.Status = InsuranceExpired
		case days <= p.ReminderDays:
			view.Status = InsuranceExpiring
		default:
			view.Status = InsuranceValid
		}
	}
	return view
}

// VehicleBlock is the one rule for whether a vehicle may carry jobs ("" = yes). It must agree
// with DispatchablePredicate, which applies the same rule in SQL.
func VehicleBlock(v *ent.Vehicle, p InsurancePolicy, now time.Time) string {
	if v == nil {
		return ""
	}
	if v.Status != "active" {
		return BlockVehicleInactive
	}
	if !NeedsInsurance(v.VehicleType) {
		return ""
	}
	if v.InsuranceExpiry == nil {
		if p.BlockUninsured {
			return BlockNoInsurance
		}
		return ""
	}
	if !v.InsuranceExpiry.After(now) && p.BlockExpired {
		return BlockInsuranceExpired
	}
	return ""
}

// DispatchablePredicate matches active riders who may take jobs: no vehicle, or a vehicle that
// passes VehicleBlock. It keeps dispatch one indexed query however large the fleet grows.
func DispatchablePredicate(p InsurancePolicy, now time.Time) predicate.FleetMember {
	vehicleOK := []predicate.Vehicle{entvehicle.Status("active")}
	if p.BlockUninsured {
		vehicleOK = append(vehicleOK, entvehicle.Or(entvehicle.VehicleType("bicycle"), entvehicle.InsuranceExpiryNotNil()))
	}
	if p.BlockExpired {
		vehicleOK = append(vehicleOK, entvehicle.Or(entvehicle.VehicleType("bicycle"), entvehicle.InsuranceExpiryIsNil(), entvehicle.InsuranceExpiryGT(now)))
	}
	return fleetmember.And(
		fleetmember.Status("active"),
		fleetmember.Or(fleetmember.VehicleIDIsNil(), fleetmember.HasVehicleWith(vehicleOK...)),
	)
}

// ErrRiderBlocked is returned when a job is given to a rider whose vehicle may not carry jobs.
type ErrRiderBlocked struct {
	Reason string
	Plate  string
}

func (e *ErrRiderBlocked) Error() string { return BlockMessage(e.Reason, e.Plate) }

// BlockMessage explains a block reason to dispatchers and riders.
func BlockMessage(reason, plate string) string {
	if plate == "" {
		plate = "the rider's vehicle"
	}
	switch reason {
	case BlockVehicleInactive:
		return fmt.Sprintf("%s is not active, so this rider gets no jobs", plate)
	case BlockNoInsurance:
		return fmt.Sprintf("%s has no insurance recorded, so this rider gets no jobs until it is added", plate)
	case BlockInsuranceExpired:
		return fmt.Sprintf("the insurance of %s has expired, so this rider gets no jobs until it is renewed", plate)
	}
	return ""
}

// RiderBlock returns why a rider may not take jobs because of their vehicle (nil = allowed). The
// task module calls it before every assignment and claim.
func RiderBlock(ctx context.Context, client *ent.Client, tenantID uuid.UUID, m *ent.FleetMember) error {
	if m == nil || m.VehicleID == nil {
		return nil
	}
	v := m.Edges.Vehicle
	if v == nil {
		var err error
		if v, err = client.Vehicle.Get(ctx, *m.VehicleID); err != nil {
			if ent.IsNotFound(err) {
				return nil
			}
			return fmt.Errorf("fleet: load rider vehicle: %w", err)
		}
	}
	if reason := VehicleBlock(v, LoadInsurancePolicy(ctx, client, tenantID), time.Now()); reason != "" {
		return &ErrRiderBlocked{Reason: reason, Plate: v.LicensePlate}
	}
	return nil
}

// ListDispatchable returns the riders who may take jobs now, with user and vehicle loaded.
func (s *Service) ListDispatchable(ctx context.Context, tenantID uuid.UUID, limit int) ([]*ent.FleetMember, error) {
	p := LoadInsurancePolicy(ctx, s.client, tenantID)
	members, err := s.client.FleetMember.Query().
		Where(fleetmember.TenantID(tenantID), DispatchablePredicate(p, time.Now())).
		WithUser().
		WithVehicle().
		Order(ent.Asc(fleetmember.FieldCreatedAt)).
		Limit(limit).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("fleet: list dispatchable riders: %w", err)
	}
	return members, nil
}

// InsuranceInput is a recorded or renewed policy. Dates are YYYY-MM-DD as on the certificate.
type InsuranceInput struct {
	PolicyNo      string `json:"policy_no"`
	CertificateNo string `json:"certificate_no,omitempty"`
	Insurer       string `json:"insurer,omitempty"`
	Cover         string `json:"cover,omitempty"`
	StartsOn      string `json:"starts_on,omitempty"`
	ExpiresOn     string `json:"expires_on"`
	// Document is the certificate photo or PDF (an upload reference).
	Document string `json:"document,omitempty"`
}

// ErrInvalidInsurance marks input the caller got wrong (400).
var ErrInvalidInsurance = errors.New("fleet: invalid insurance")

func parseDay(s string) (time.Time, error) {
	return time.ParseInLocation("2006-01-02", strings.TrimSpace(s), localtime.Location)
}

// validInsurance is an InsuranceInput that passed validate.
type validInsurance struct {
	ins     Insurance
	expires time.Time // end of the expiry day, local
	doc     string
}

// validate checks the input and returns what will be stored. A policy that already expired is
// refused: the point of recording one is a vehicle that may carry jobs.
func (in InsuranceInput) validate(now time.Time, actor string) (validInsurance, error) {
	in.PolicyNo = strings.TrimSpace(in.PolicyNo)
	if in.PolicyNo == "" {
		return validInsurance{}, fmt.Errorf("%w: policy number is required", ErrInvalidInsurance)
	}
	expires, err := parseDay(in.ExpiresOn)
	if err != nil {
		return validInsurance{}, fmt.Errorf("%w: expiry date must be YYYY-MM-DD", ErrInvalidInsurance)
	}
	if localtime.DaysUntil(now, expires) < 0 {
		return validInsurance{}, fmt.Errorf("%w: this policy has already expired; enter the renewed one", ErrInvalidInsurance)
	}
	startsOn := ""
	if strings.TrimSpace(in.StartsOn) != "" {
		starts, err := parseDay(in.StartsOn)
		if err != nil {
			return validInsurance{}, fmt.Errorf("%w: start date must be YYYY-MM-DD", ErrInvalidInsurance)
		}
		if starts.After(expires) {
			return validInsurance{}, fmt.Errorf("%w: the policy cannot start after it expires", ErrInvalidInsurance)
		}
		startsOn = starts.Format("2006-01-02")
	}
	return validInsurance{
		ins: Insurance{
			PolicyNo:      in.PolicyNo,
			CertificateNo: strings.TrimSpace(in.CertificateNo),
			Insurer:       strings.TrimSpace(in.Insurer),
			Cover:         strings.TrimSpace(in.Cover),
			StartsOn:      startsOn,
			UpdatedBy:     actor,
			UpdatedAt:     now.UTC().Format(time.RFC3339),
		},
		expires: localtime.EndOfDay(expires),
		doc:     strings.TrimSpace(in.Document),
	}, nil
}

// RecordInsurance saves a vehicle's policy (new or renewed). A vehicle the monitor took off jobs
// for an expired policy is reactivated; the previous policy is kept in a short history.
func (s *Service) RecordInsurance(ctx context.Context, tenantID, vehicleID uuid.UUID, in InsuranceInput, actor string) (*ent.Vehicle, error) {
	vi, err := in.validate(time.Now(), actor)
	if err != nil {
		return nil, err
	}
	v, err := s.client.Vehicle.Query().Where(entvehicle.ID(vehicleID), entvehicle.TenantID(tenantID)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrVehicleAbsent
		}
		return nil, fmt.Errorf("fleet: load vehicle: %w", err)
	}
	return applyInsurance(ctx, s.client.Vehicle, v, vi)
}

// applyInsurance writes a validated policy onto v through vc (the client or a transaction's).
func applyInsurance(ctx context.Context, vc *ent.VehicleClient, v *ent.Vehicle, vi validInsurance) (*ent.Vehicle, error) {
	meta := map[string]any{}
	for k, val := range v.Metadata {
		meta[k] = val
	}
	if prev := InsuranceOf(v); prev.PolicyNo != "" {
		hist, _ := meta[metaInsuranceHistory].([]any)
		entry := prev.toMap()
		if v.InsuranceExpiry != nil {
			entry["expires_on"] = localtime.Date(*v.InsuranceExpiry)
		}
		hist = append([]any{entry}, hist...)
		if len(hist) > insuranceHistoryLimit {
			hist = hist[:insuranceHistoryLimit]
		}
		meta[metaInsuranceHistory] = hist
	}
	meta[metaInsurance] = vi.ins.toMap()

	upd := vc.UpdateOne(v).SetInsuranceExpiry(vi.expires)
	if vi.doc != "" {
		upd.SetInsuranceDocument(vi.doc)
	}
	if v.Status == "inactive" && meta[metaInactiveReason] == InactiveInsuranceExpired {
		upd.SetStatus("active")
		delete(meta, metaInactiveReason)
	}
	saved, err := upd.SetMetadata(meta).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("fleet: save insurance: %w", err)
	}
	return saved, nil
}

// RiderVehicleID returns the vehicle linked to the rider with this local user id.
func (s *Service) RiderVehicleID(ctx context.Context, tenantID, userID uuid.UUID) (uuid.UUID, error) {
	m, err := s.client.FleetMember.Query().
		Where(fleetmember.TenantID(tenantID), fleetmember.UserID(userID)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return uuid.Nil, fmt.Errorf("fleet: member not found")
		}
		return uuid.Nil, err
	}
	if m.VehicleID == nil {
		return uuid.Nil, ErrVehicleAbsent
	}
	return *m.VehicleID, nil
}
