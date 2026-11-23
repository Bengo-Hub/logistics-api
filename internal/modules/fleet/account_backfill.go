package fleet

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	sharedcache "github.com/Bengo-Hub/cache"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/fleetmember"
	entuser "github.com/bengobox/logistics-service/internal/ent/user"
	"github.com/bengobox/logistics-service/internal/platform/authapi"
)

// accountBackfillInterval is how often riders without an SSO account are looked for. The work is
// claimed by one replica per period.
const accountBackfillInterval = time.Hour

// backfillStatuses are the riders who should be able to sign in: working or waiting to work.
var backfillStatuses = []string{"active", "approved", "inactive"}

// StartRiderAccountBackfill gives every working rider who has no SSO account one, at start-up and
// then hourly. Riders onboarded before admins' onboarding created the account (2026-10-17, fixed
// 2026-10-18) were saved under a local stub with a random key: they show in logistics but not in
// SSO, cannot sign in to the rider app and get no job alerts (Urban Loft's Phelix Omundo).
func (s *Service) StartRiderAccountBackfill(ctx context.Context) {
	s.linkMissingRiderAccounts(ctx)
	t := time.NewTicker(accountBackfillInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.linkMissingRiderAccounts(ctx)
		}
	}
}

func (s *Service) linkMissingRiderAccounts(ctx context.Context) {
	if s.auth == nil || !s.auth.Enabled() {
		return
	}
	if !sharedcache.ClaimPeriod(ctx, "logistics:rider-account-backfill", accountBackfillInterval) {
		return
	}
	members, err := s.client.FleetMember.Query().
		Where(
			fleetmember.StatusIn(backfillStatuses...),
			fleetmember.HasUserWith(entuser.AuthServiceUserIDIsNil()),
		).
		WithUser().
		Limit(200).
		All(ctx)
	if err != nil {
		s.log.Warn("rider account backfill: list riders", zap.Error(err))
		return
	}
	for _, m := range members {
		if err := s.linkRiderAccount(ctx, m); err != nil {
			s.log.Warn("rider account backfill: rider not linked",
				zap.String("member_id", m.ID.String()), zap.Error(err))
		}
	}
}

// linkRiderAccount creates (or finds) the rider's auth account, moves the local stub onto the auth
// id (fleet membership, role grants and tasks follow) and emails the sign-in details when auth-api
// created the account just now.
func (s *Service) linkRiderAccount(ctx context.Context, m *ent.FleetMember) error {
	u := m.Edges.User
	if u == nil || strings.TrimSpace(u.Email) == "" || strings.HasSuffix(u.Email, "@invalid.local") {
		return nil
	}
	account, err := s.auth.AddMember(ctx, m.TenantID, authapi.MemberRequest{
		Email:       u.Email,
		Name:        u.FullName,
		Phone:       u.Phone,
		Roles:       []string{authapi.RoleDriver},
		MergeRoles:  true,
		SkipWelcome: true,
	})
	if err != nil {
		return err
	}
	if err := s.rekeyInTx(ctx, u, account.UserID); err != nil {
		return err
	}
	s.ensureDriverRole(ctx, m.TenantID, account.UserID)

	emailed := false
	if account.TempPassword != "" && s.mail != nil {
		slug := s.tenantSlugFor(ctx, m.TenantID, "")
		if fl, ferr := s.client.Fleet.Get(ctx, m.FleetID); ferr == nil {
			slug = s.healFleetSlug(ctx, fl, slug).TenantSlug
		}
		first := strings.Fields(u.FullName)
		name := u.FullName
		if len(first) > 0 {
			name = first[0]
		}
		emailed = s.sendRiderAccountEmail(ctx, m.TenantID, slug, m.ID, OnboardRiderRequest{FirstName: name, Email: u.Email}, account.TempPassword)
	}
	s.log.Info("rider account linked",
		zap.String("member_id", m.ID.String()),
		zap.String("auth_user_id", account.UserID.String()),
		zap.Bool("new_account", account.TempPassword != ""),
		zap.Bool("email_sent", emailed))
	return nil
}

func (s *Service) rekeyInTx(ctx context.Context, stub *ent.User, authID uuid.UUID) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := RekeyUser(ctx, tx, stub, authID, RekeyFields{Role: authapi.RoleDriver}); err != nil {
		return err
	}
	return tx.Commit()
}
