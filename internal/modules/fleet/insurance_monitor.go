package fleet

import (
	"context"
	"time"

	sharedcache "github.com/Bengo-Hub/cache"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/predicate"
	entvehicle "github.com/bengobox/logistics-service/internal/ent/vehicle"
	"github.com/bengobox/logistics-service/internal/platform/events"
	"github.com/bengobox/logistics-service/internal/shared/localtime"
)

const (
	insuranceCheckInterval = time.Hour
	// expiredLookback bounds the expired scan: a policy that lapsed longer ago was handled then.
	expiredLookback = 30 * 24 * time.Hour
	insuranceBatch  = 500
)

// StartInsuranceMonitor checks vehicle insurance every hour on one replica: a reminder the
// tenant's number of days (default 7) before a policy expires, and on expiry the vehicle is
// taken off jobs (when the tenant blocks expired insurance) with a notice. Rider and tenant get
// each notice by email and WhatsApp through notifications-api.
func (s *Service) StartInsuranceMonitor(ctx context.Context) {
	ticker := time.NewTicker(insuranceCheckInterval)
	defer ticker.Stop()
	for {
		if sharedcache.ClaimPeriod(ctx, "logistics:insurance-monitor", insuranceCheckInterval) {
			s.checkInsurance(ctx, time.Now())
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// checkInsurance runs one pass. Both scans use the insurance_expiry index and read in batches.
func (s *Service) checkInsurance(ctx context.Context, now time.Time) {
	policies := map[uuid.UUID]InsurancePolicy{}
	policyOf := func(tenantID uuid.UUID) InsurancePolicy {
		p, ok := policies[tenantID]
		if !ok {
			p = LoadInsurancePolicy(ctx, s.client, tenantID)
			policies[tenantID] = p
		}
		return p
	}

	s.scanVehicles(ctx, entvehicle.And(
		entvehicle.InsuranceExpiryGT(now),
		entvehicle.InsuranceExpiryLTE(now.Add(maxReminderDays*24*time.Hour)),
	), func(v *ent.Vehicle) {
		p := policyOf(v.TenantID)
		in := InsuranceOf(v)
		expiresOn := localtime.Date(*v.InsuranceExpiry)
		days := localtime.DaysUntil(now, *v.InsuranceExpiry)
		if days > p.ReminderDays || in.RemindedFor == expiresOn {
			return
		}
		if s.notifyInsurance(ctx, v, p, events.EventVehicleInsuranceExpiring, days) {
			in.RemindedFor = expiresOn
			s.saveInsuranceMeta(ctx, v, in, false)
		}
	})

	s.scanVehicles(ctx, entvehicle.And(
		entvehicle.InsuranceExpiryLTE(now),
		entvehicle.InsuranceExpiryGT(now.Add(-expiredLookback)),
	), func(v *ent.Vehicle) {
		p := policyOf(v.TenantID)
		in := InsuranceOf(v)
		expiresOn := localtime.Date(*v.InsuranceExpiry)
		deactivate := p.BlockExpired && NeedsInsurance(v.VehicleType) && v.Status == "active"
		if in.ExpiredNoticeFor == expiresOn && !deactivate {
			return
		}
		if in.ExpiredNoticeFor != expiresOn && s.notifyInsurance(ctx, v, p, events.EventVehicleInsuranceExpired, localtime.DaysUntil(now, *v.InsuranceExpiry)) {
			in.ExpiredNoticeFor = expiresOn
		}
		s.saveInsuranceMeta(ctx, v, in, deactivate)
		if deactivate {
			s.log.Info("vehicle deactivated: insurance expired",
				zap.String("vehicle_id", v.ID.String()), zap.String("plate", v.LicensePlate), zap.String("expired_on", expiresOn))
		}
	})
}

// scanVehicles walks the vehicles matching pred in id order, batch by batch, with their rider.
func (s *Service) scanVehicles(ctx context.Context, pred predicate.Vehicle, fn func(*ent.Vehicle)) {
	var after uuid.UUID
	for {
		q := s.client.Vehicle.Query().
			Where(pred, entvehicle.InsuranceExpiryNotNil()).
			WithMembers(func(mq *ent.FleetMemberQuery) { mq.WithUser() }).
			Order(ent.Asc(entvehicle.FieldID)).
			Limit(insuranceBatch)
		if after != uuid.Nil {
			q = q.Where(entvehicle.IDGT(after))
		}
		batch, err := q.All(ctx)
		if err != nil {
			s.log.Error("insurance monitor: scan vehicles", zap.Error(err))
			return
		}
		for _, v := range batch {
			fn(v)
		}
		if len(batch) < insuranceBatch {
			return
		}
		after = batch[len(batch)-1].ID
	}
}

// saveInsuranceMeta stores the notice markers and, when asked, takes the vehicle off jobs.
func (s *Service) saveInsuranceMeta(ctx context.Context, v *ent.Vehicle, in Insurance, deactivate bool) {
	meta := map[string]any{}
	for k, val := range v.Metadata {
		meta[k] = val
	}
	meta[metaInsurance] = in.toMap()
	upd := s.client.Vehicle.UpdateOne(v).SetMetadata(meta)
	if deactivate {
		meta[metaInactiveReason] = InactiveInsuranceExpired
		upd.SetMetadata(meta).SetStatus("inactive")
	}
	if err := upd.Exec(ctx); err != nil {
		s.log.Error("insurance monitor: save vehicle", zap.String("vehicle_id", v.ID.String()), zap.Error(err))
	}
}

// notifyInsurance publishes the reminder or expiry notice. False when it could not be queued, so
// the next pass tries again.
func (s *Service) notifyInsurance(ctx context.Context, v *ent.Vehicle, p InsurancePolicy, eventType string, daysLeft int) bool {
	if s.publisher == nil {
		return false
	}
	in := InsuranceOf(v)
	d := events.VehicleInsuranceEventData{
		VehicleID:    v.ID.String(),
		LicensePlate: v.LicensePlate,
		VehicleType:  v.VehicleType,
		PolicyNo:     in.PolicyNo,
		Insurer:      in.Insurer,
		ExpiresOn:    localtime.Date(*v.InsuranceExpiry),
		DaysLeft:     daysLeft,
		Blocked:      p.BlockExpired && NeedsInsurance(v.VehicleType),
		TenantSlug:   s.resolveTenantSlug(ctx, v.FleetID),
	}
	if len(v.Edges.Members) > 0 {
		if u := v.Edges.Members[0].Edges.User; u != nil {
			d.RiderName, d.RiderEmail, d.RiderPhone = u.FullName, u.Email, u.Phone
		}
	}
	if err := s.publisher.PublishVehicleInsurance(ctx, v.TenantID, eventType, d); err != nil {
		s.log.Warn("insurance monitor: publish", zap.String("event", eventType), zap.Error(err))
		return false
	}
	return true
}
