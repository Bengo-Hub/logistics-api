package tasks

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/fleetmember"
	"github.com/bengobox/logistics-service/internal/ent/predicate"
	"github.com/bengobox/logistics-service/internal/ent/proofofdelivery"
	"github.com/bengobox/logistics-service/internal/ent/task"
	entuser "github.com/bengobox/logistics-service/internal/ent/user"
)

// Rider cash ledger. A rider who takes cash on delivery holds the business's money until they
// hand it in at the outlet. The ledger is the cash proofs of delivery not yet stamped as handed
// in (metadata remitted_at); recording a hand-in stamps them with one remittance id, the amount
// expected and the amount actually received, so a shortfall stays visible on each delivery.

// ErrNothingToRemit means the rider holds no cash from deliveries.
var ErrNothingToRemit = errors.New("tasks: this rider has no cash to hand in")

// CashDelivery is one cash-on-delivery drop-off whose cash the rider still holds.
type CashDelivery struct {
	PoDID       uuid.UUID `json:"pod_id"`
	TaskID      uuid.UUID `json:"task_id"`
	OrderNumber string    `json:"order_number,omitempty"`
	Amount      float64   `json:"amount"`
	DeliveredAt time.Time `json:"delivered_at"`
}

// RiderCash is the cash one rider holds.
type RiderCash struct {
	FleetMemberID uuid.UUID      `json:"fleet_member_id"`
	RiderName     string         `json:"rider_name,omitempty"`
	RiderPhone    string         `json:"rider_phone,omitempty"`
	Held          float64        `json:"held"`
	Deliveries    []CashDelivery `json:"deliveries"`
	OldestAt      *time.Time     `json:"oldest_at,omitempty"`
}

// Remittance is a recorded hand-in.
type Remittance struct {
	ID            uuid.UUID `json:"id"`
	FleetMemberID uuid.UUID `json:"fleet_member_id"`
	Deliveries    int       `json:"deliveries"`
	Expected      float64   `json:"expected"`
	Received      float64   `json:"received"`
	Shortfall     float64   `json:"shortfall"`
	ReceivedBy    string    `json:"received_by,omitempty"`
	At            time.Time `json:"at"`
}

// notRemitted matches proofs of delivery whose cash has not been handed in yet.
func notRemitted() predicate.ProofOfDelivery {
	return predicate.ProofOfDelivery(func(s *sql.Selector) {
		s.Where(sql.P(func(b *sql.Builder) {
			b.WriteString("(")
			b.WriteString(s.C(proofofdelivery.FieldMetadata))
			b.WriteString("->>'remitted_at' IS NULL)")
		}))
	})
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// outstandingCash loads the cash deliveries not yet handed in, for one rider or (memberID nil)
// every rider of the tenant.
func (s *Service) outstandingCash(ctx context.Context, tenantID uuid.UUID, memberID *uuid.UUID) ([]*ent.ProofOfDelivery, map[uuid.UUID]*ent.Task, error) {
	preds := []predicate.ProofOfDelivery{
		proofofdelivery.TenantID(tenantID),
		proofofdelivery.CollectionMethod("cash"),
		proofofdelivery.AmountCollectedGT(0),
		notRemitted(),
	}
	if memberID != nil {
		preds = append(preds, proofofdelivery.FleetMemberID(*memberID))
	}
	pods, err := s.client.ProofOfDelivery.Query().
		Where(preds...).
		Order(ent.Asc(proofofdelivery.FieldCapturedAt)).
		All(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("tasks: load cash deliveries: %w", err)
	}
	taskIDs := make([]uuid.UUID, 0, len(pods))
	for _, p := range pods {
		taskIDs = append(taskIDs, p.TaskID)
	}
	byID := map[uuid.UUID]*ent.Task{}
	if len(taskIDs) > 0 {
		list, terr := s.client.Task.Query().Where(task.IDIn(taskIDs...)).All(ctx)
		if terr != nil {
			return nil, nil, fmt.Errorf("tasks: load cash delivery tasks: %w", terr)
		}
		for _, t := range list {
			byID[t.ID] = t
		}
	}
	return pods, byID, nil
}

// cashAmount is what the rider owes for a delivery: the order's cash-on-delivery amount (the
// customer may have handed over more and got change), else what was recorded as collected.
func cashAmount(p *ent.ProofOfDelivery, t *ent.Task) float64 {
	if t != nil {
		if due := taskCODAmount(t); due > 0 && due <= p.AmountCollected {
			return due
		}
	}
	return p.AmountCollected
}

func (s *Service) buildRiderCash(ctx context.Context, tenantID uuid.UUID, pods []*ent.ProofOfDelivery, tasksByID map[uuid.UUID]*ent.Task) []RiderCash {
	byMember := map[uuid.UUID]*RiderCash{}
	order := []uuid.UUID{}
	for _, p := range pods {
		rc, ok := byMember[p.FleetMemberID]
		if !ok {
			rc = &RiderCash{FleetMemberID: p.FleetMemberID, Deliveries: []CashDelivery{}}
			byMember[p.FleetMemberID] = rc
			order = append(order, p.FleetMemberID)
		}
		t := tasksByID[p.TaskID]
		amount := cashAmount(p, t)
		rc.Held = round2(rc.Held + amount)
		orderNo := ""
		if t != nil {
			orderNo = metadataString(t.Metadata, "order_number")
		}
		rc.Deliveries = append(rc.Deliveries, CashDelivery{
			PoDID: p.ID, TaskID: p.TaskID, OrderNumber: orderNo, Amount: amount, DeliveredAt: p.CapturedAt,
		})
		if rc.OldestAt == nil || p.CapturedAt.Before(*rc.OldestAt) {
			at := p.CapturedAt
			rc.OldestAt = &at
		}
	}

	// Rider names for the dispatcher's list.
	if len(order) > 0 {
		members, _ := s.client.FleetMember.Query().
			Where(fleetmember.TenantID(tenantID), fleetmember.IDIn(order...)).
			All(ctx)
		userIDs := make([]uuid.UUID, 0, len(members))
		memberUser := map[uuid.UUID]uuid.UUID{}
		for _, m := range members {
			userIDs = append(userIDs, m.UserID)
			memberUser[m.ID] = m.UserID
		}
		users, _ := s.client.User.Query().Where(entuser.IDIn(userIDs...)).All(ctx)
		userByID := map[uuid.UUID]*ent.User{}
		for _, u := range users {
			userByID[u.ID] = u
		}
		for id, rc := range byMember {
			if u := userByID[memberUser[id]]; u != nil {
				rc.RiderName = u.FullName
				rc.RiderPhone = u.Phone
			}
		}
	}

	out := make([]RiderCash, 0, len(order))
	for _, id := range order {
		out = append(out, *byMember[id])
	}
	// Most cash first: that is who the outlet should chase.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Held > out[j].Held })
	return out
}

// RiderCashHeld returns the cash one rider holds from deliveries.
func (s *Service) RiderCashHeld(ctx context.Context, tenantID, memberID uuid.UUID) (RiderCash, error) {
	pods, tasksByID, err := s.outstandingCash(ctx, tenantID, &memberID)
	if err != nil {
		return RiderCash{}, err
	}
	list := s.buildRiderCash(ctx, tenantID, pods, tasksByID)
	if len(list) == 0 {
		return RiderCash{FleetMemberID: memberID, Deliveries: []CashDelivery{}}, nil
	}
	return list[0], nil
}

// CashWithRiders returns every rider still holding delivery cash, most first.
func (s *Service) CashWithRiders(ctx context.Context, tenantID uuid.UUID) ([]RiderCash, error) {
	pods, tasksByID, err := s.outstandingCash(ctx, tenantID, nil)
	if err != nil {
		return nil, err
	}
	return s.buildRiderCash(ctx, tenantID, pods, tasksByID), nil
}

// RecordRemittance records a rider handing in their delivery cash. Every outstanding cash
// delivery is stamped with the same remittance, the amount expected and the amount received;
// a shortfall is returned (and kept on each delivery) for the manager to follow up.
func (s *Service) RecordRemittance(ctx context.Context, tenantID, memberID uuid.UUID, received float64, receivedBy, notes string) (Remittance, error) {
	if received < 0 {
		return Remittance{}, fmt.Errorf("tasks: amount received cannot be negative")
	}
	pods, tasksByID, err := s.outstandingCash(ctx, tenantID, &memberID)
	if err != nil {
		return Remittance{}, err
	}
	if len(pods) == 0 {
		return Remittance{}, ErrNothingToRemit
	}
	expected := 0.0
	for _, p := range pods {
		expected += cashAmount(p, tasksByID[p.TaskID])
	}
	expected = round2(expected)
	received = round2(received)
	shortfall := round2(math.Max(0, expected-received))

	rem := Remittance{
		ID: uuid.New(), FleetMemberID: memberID, Deliveries: len(pods),
		Expected: expected, Received: received, Shortfall: shortfall,
		ReceivedBy: receivedBy, At: time.Now().UTC(),
	}
	stamp := map[string]any{
		"remitted_at":          rem.At.Format(time.RFC3339),
		"remittance_id":        rem.ID.String(),
		"remittance_expected":  expected,
		"remittance_received":  received,
		"remittance_shortfall": shortfall,
		"remitted_to":          receivedBy,
	}
	if n := strings.TrimSpace(notes); n != "" {
		stamp["remittance_notes"] = n
	}
	for _, p := range pods {
		meta := map[string]any{}
		for k, v := range p.Metadata {
			meta[k] = v
		}
		for k, v := range stamp {
			meta[k] = v
		}
		if _, uerr := s.client.ProofOfDelivery.UpdateOne(p).SetMetadata(meta).Save(ctx); uerr != nil {
			return Remittance{}, fmt.Errorf("tasks: record hand-in: %w", uerr)
		}
	}
	s.log.Info("rider cash handed in",
		zap.String("member_id", memberID.String()),
		zap.Float64("expected", expected),
		zap.Float64("received", received),
		zap.Int("deliveries", len(pods)))
	return rem, nil
}
