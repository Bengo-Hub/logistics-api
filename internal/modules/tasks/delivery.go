package tasks

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/task"
	"github.com/bengobox/logistics-service/internal/ent/taskassignment"
	fleetmod "github.com/bengobox/logistics-service/internal/modules/fleet"
	"github.com/bengobox/logistics-service/internal/platform/events"
)

// Closing a delivery. There are three ways a task becomes "delivered", and all of them go
// through completeDelivery, so ordering and pos see the same task.completed event either way:
//
//   - the rider submits proof of delivery with the customer's 6-digit code (SubmitPoD);
//   - a dispatcher enters the customer's code for the rider, for example when the rider's phone
//     is dead (SubmitPoD again, with a dispatcher actor; the same code check applies);
//   - an admin with logistics.tasks.override marks it delivered without the code, giving a
//     reason (OverrideDelivery). The override is stamped on the task, the proof and the event.
//
// The code is checked here and nowhere else. The rider app only checks that six digits were
// typed. After MaxCodeAttempts wrong codes the task stops accepting codes and the dispatcher is
// alerted, so the code cannot be guessed.

// MaxCodeAttempts is how many wrong delivery codes a task accepts before it locks.
const MaxCodeAttempts = 5

const codeAttemptsKey = "pod_code_failed_attempts"

var (
	// ErrCodeRequired means the customer has a delivery code and none was entered.
	ErrCodeRequired = errors.New("tasks: enter the customer's 6-digit delivery code")
	// ErrCodeWrong means the code entered does not match the customer's code.
	ErrCodeWrong = errors.New("tasks: incorrect delivery confirmation code")
	// ErrCodeLocked means too many wrong codes were entered; a dispatcher must close the job.
	ErrCodeLocked = errors.New("tasks: too many wrong delivery codes; the dispatcher has been asked to confirm this delivery")
	// ErrRecipientRequired means nobody was named as receiving the order.
	ErrRecipientRequired = errors.New("tasks: enter who received the order")
	// ErrNoRider means no rider holds the task, so there is nobody to record the delivery against.
	ErrNoRider = errors.New("tasks: no rider holds this delivery; assign the rider who took it first")
	// ErrNotCollected means the order has not left the outlet yet.
	ErrNotCollected = errors.New("tasks: the order has not been picked up yet")
	// ErrProofExists means proof of delivery was already recorded for this task.
	ErrProofExists = errors.New("tasks: proof of delivery was already recorded for this delivery; refresh to see it")
)

// OverrideDeliveryRequest is an admin marking a delivery done without the customer's code.
type OverrideDeliveryRequest struct {
	Reason        string `json:"reason"`
	RecipientName string `json:"recipient_name,omitempty"`
	Notes         string `json:"notes,omitempty"`
	// Cash on delivery taken by the rider (required when the order is pay on delivery).
	AmountCollected     float64 `json:"amount_collected,omitempty"`
	CollectionMethod    string  `json:"collection_method,omitempty"`
	CollectionReference string  `json:"collection_reference,omitempty"`
}

// codCollection is the checked cash-on-delivery part of a delivery.
type codCollection struct {
	Due       float64
	Amount    float64
	Method    string
	Reference string
}

func (c codCollection) collected() bool { return c.Due > 0 && c.Amount >= c.Due }

// checkCOD validates how a pay-on-delivery order was paid at the door: the full amount, in cash
// or by M-Pesa to the business with its 10-character confirmation code. Prepaid orders record no
// tender, whatever the client sent, so ordering never books a payment that did not happen.
func checkCOD(due, amount float64, method, reference string) (codCollection, error) {
	if due <= 0 {
		return codCollection{}, nil
	}
	method = strings.ToLower(strings.TrimSpace(method))
	reference = strings.ToUpper(strings.Join(strings.Fields(reference), ""))
	if amount < due {
		return codCollection{}, fmt.Errorf("tasks: amount collected (%.2f) is less than the %.2f due", amount, due)
	}
	if method == "" {
		method = "cash"
	}
	if method != "cash" && method != "mpesa" {
		return codCollection{}, fmt.Errorf("tasks: collection method must be cash or mpesa")
	}
	if method == "mpesa" && len(reference) != 10 {
		return codCollection{}, fmt.Errorf("tasks: enter the 10-character M-Pesa code for this payment")
	}
	if method == "cash" {
		reference = ""
	}
	return codCollection{Due: due, Amount: amount, Method: method, Reference: reference}, nil
}

// normalizeCode keeps only the digits of an entered code ("123 456" and "123-456" are 123456).
func normalizeCode(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// codeMatches compares the entered code with the customer's in constant time.
func codeMatches(stored, entered string) bool {
	stored, entered = normalizeCode(stored), normalizeCode(entered)
	if stored == "" || entered == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(stored), []byte(entered)) == 1
}

// codeAttempts returns the wrong codes already entered for a task.
func codeAttempts(t *ent.Task) int {
	if t == nil || t.Metadata == nil {
		return 0
	}
	return int(metadataNumber(t.Metadata[codeAttemptsKey]))
}

// deliveryCompletion is everything completeDelivery records.
type deliveryCompletion struct {
	actor     Actor
	via       string // code | recipient | override
	recipient string
	notes     string
	photo     string
	signature string
	otp       string
	cod       codCollection
	podMeta   map[string]any
	taskMeta  map[string]any
	override  string // reason, for via == override
}

// SubmitPoD records proof of delivery and marks the task delivered. The customer's code, when
// the order has one, is verified here; the rider app's check is only a convenience.
func (s *Service) SubmitPoD(ctx context.Context, tenantID, taskID uuid.UUID, req SubmitPoDRequest) (*ent.ProofOfDelivery, error) {
	t, err := s.loadTask(ctx, tenantID, taskID)
	if err != nil {
		return nil, err
	}
	if IsTerminal(t.Status) {
		return nil, ErrTaskClosed
	}
	if !podAllowedFrom[t.Status] {
		return nil, fmt.Errorf("%w (status %s)", ErrNotCollected, t.Status)
	}
	cod, err := checkCOD(taskCODAmount(t), req.AmountCollected, req.CollectionMethod, req.CollectionReference)
	if err != nil {
		return nil, err
	}
	actor := req.Actor
	if actor.Type == "" {
		actor.Type = "rider"
	}

	recipient := strings.TrimSpace(req.RecipientName)
	photo, inlinePhoto := podPhotoURL(req.PhotoURL)
	via := "recipient"
	if podCode := metadataString(t.Metadata, "pod_code"); podCode != "" {
		if codeAttempts(t) >= MaxCodeAttempts {
			return nil, ErrCodeLocked
		}
		if normalizeCode(req.ConfirmationCode) == "" {
			return nil, ErrCodeRequired
		}
		if !codeMatches(podCode, req.ConfirmationCode) {
			return nil, s.wrongCode(ctx, t, actor)
		}
		if recipient == "" {
			return nil, ErrRecipientRequired
		}
		via = "code"
	} else if recipient == "" && photo == "" && strings.TrimSpace(req.SignatureURL) == "" {
		// No customer code (till and transfer deliveries): some proof is still needed.
		return nil, ErrRecipientRequired
	}

	podMeta := map[string]any{}
	for k, v := range req.Metadata {
		podMeta[k] = v
	}
	if req.Latitude != nil && req.Longitude != nil {
		podMeta["latitude"] = *req.Latitude
		podMeta["longitude"] = *req.Longitude
	}
	if inlinePhoto {
		// Older rider-app builds sent the photo itself as a base64 data URL (100 KB+ per
		// delivery in the row). The app now uploads it first; an inline photo is dropped
		// rather than blocking the delivery at the door.
		podMeta["photo_dropped"] = "inline photo not stored; upload it to /media/upload first"
	}

	return s.completeDelivery(ctx, t, deliveryCompletion{
		actor:     actor,
		via:       via,
		recipient: recipient,
		notes:     strings.TrimSpace(req.Notes),
		photo:     photo,
		signature: strings.TrimSpace(req.SignatureURL),
		otp:       req.OTPCode,
		cod:       cod,
		podMeta:   podMeta,
	})
}

// OverrideDelivery lets an admin mark a delivery done without the customer's code (the customer
// lost it, the rider's phone died after handing over). A reason is required and, for a pay on
// delivery order, how the rider was paid. The delivery is recorded against the rider holding
// the job, so cash taken still shows in that rider's cash to hand in. Permission
// (logistics.tasks.override) is checked by the route.
func (s *Service) OverrideDelivery(ctx context.Context, tenantID, taskID uuid.UUID, req OverrideDeliveryRequest, actor Actor) (*ent.ProofOfDelivery, error) {
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		return nil, ErrReasonRequired
	}
	t, err := s.loadTask(ctx, tenantID, taskID)
	if err != nil {
		return nil, err
	}
	if IsTerminal(t.Status) {
		return nil, ErrTaskClosed
	}
	if !IsInProgress(t.Status) {
		return nil, ErrNoRider
	}
	cod, err := checkCOD(taskCODAmount(t), req.AmountCollected, req.CollectionMethod, req.CollectionReference)
	if err != nil {
		return nil, err
	}
	if actor.Type == "" {
		actor.Type = "dispatcher"
	}
	now := time.Now().UTC().Format(time.RFC3339)
	by := ""
	if actor.ID != uuid.Nil {
		by = actor.ID.String()
	}
	return s.completeDelivery(ctx, t, deliveryCompletion{
		actor:     actor,
		via:       "override",
		recipient: strings.TrimSpace(req.RecipientName),
		notes:     strings.TrimSpace(req.Notes),
		cod:       cod,
		override:  truncate(reason, 500),
		podMeta: map[string]any{
			"delivery_override": true,
			"override_reason":   truncate(reason, 500),
			"override_by":       by,
			"override_at":       now,
		},
		taskMeta: map[string]any{
			"delivery_override":        true,
			"delivery_override_reason": truncate(reason, 500),
			"delivery_override_by":     by,
			"delivery_override_at":     now,
		},
	})
}

// RequestDeliveryHelp is the rider telling the dispatcher they are with the customer but cannot
// complete with the code (the customer cannot find it). The dispatcher confirms with the
// customer's code or overrides; the rider never closes the job without one.
func (s *Service) RequestDeliveryHelp(ctx context.Context, tenantID, taskID, memberID uuid.UUID, note string) error {
	t, err := s.loadTask(ctx, tenantID, taskID)
	if err != nil {
		return err
	}
	if holder, ok := s.ActiveAssignee(ctx, taskID); !ok || holder != memberID {
		return ErrNotYourTask
	}
	if IsTerminal(t.Status) {
		return ErrTaskClosed
	}
	if !IsPostPickup(t.Status) {
		return ErrNotCollected
	}
	note = truncate(note, 250)
	body := fmt.Sprintf("Order %s: the rider is with the customer and cannot complete it with the delivery code.", orderLabel(t))
	if note != "" {
		body += " " + note + "."
	}
	body += " Confirm it with the customer's code or mark it delivered from the task page."
	s.alertDispatcher(ctx, tenantID, taskID, "delivery_confirmation_needed", "Rider needs the delivery confirmed", body)
	s.recordEvent(ctx, taskID, "confirmation_help_requested", Actor{ID: memberID, Type: "rider"}, map[string]any{"note": note})
	return nil
}

// wrongCode counts a wrong delivery code on the task, alerts the dispatcher when the task locks,
// and returns the error for the caller.
func (s *Service) wrongCode(ctx context.Context, t *ent.Task, actor Actor) error {
	attempts := codeAttempts(t) + 1
	if _, err := s.client.Task.Update().
		Where(task.ID(t.ID), task.StatusEQ(t.Status)).
		SetMetadata(mergeMeta(t.Metadata, map[string]any{codeAttemptsKey: attempts})).
		Save(ctx); err != nil {
		s.log.Warn("could not count a wrong delivery code", zap.String("task_id", t.ID.String()), zap.Error(err))
	}
	s.recordEvent(ctx, t.ID, "delivery_code_rejected", actor, map[string]any{"attempt": attempts})
	left := MaxCodeAttempts - attempts
	if left <= 0 {
		s.alertDispatcher(ctx, t.TenantID, t.ID, "delivery_code_locked", "Delivery code entered wrong too often",
			fmt.Sprintf("Order %s: %d wrong delivery codes were entered. Call the customer, then confirm it with their code or mark it delivered from the task page.", orderLabel(t), attempts))
		return ErrCodeLocked
	}
	return fmt.Errorf("%w (%d %s left)", ErrCodeWrong, left, plural(left, "try", "tries"))
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func (s *Service) loadTask(ctx context.Context, tenantID, taskID uuid.UUID) (*ent.Task, error) {
	t, err := s.client.Task.Query().Where(task.ID(taskID), task.TenantID(tenantID)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, fmt.Errorf("tasks: not found")
		}
		return nil, fmt.Errorf("tasks: load: %w", err)
	}
	return t, nil
}

// completeDelivery writes the proof of delivery, moves the task to delivered and ends the
// rider's assignment in one transaction (conditional on the status read, so two submissions
// cannot both win), then publishes task.completed and records the rider's earning.
func (s *Service) completeDelivery(ctx context.Context, t *ent.Task, c deliveryCompletion) (*ent.ProofOfDelivery, error) {
	memberID, ok := s.ActiveAssignee(ctx, t.ID)
	if !ok {
		return nil, ErrNoRider
	}
	now := time.Now().UTC()
	stamp := now.Format(time.RFC3339)

	podMeta := c.podMeta
	if podMeta == nil {
		podMeta = map[string]any{}
	}
	podMeta["delivered_via"] = c.via
	podMeta["confirmed_by"] = c.actor.Type
	if c.actor.ID != uuid.Nil {
		podMeta["confirmed_by_id"] = c.actor.ID.String()
	}
	if c.recipient != "" {
		podMeta["recipient_name"] = c.recipient
	}
	if c.notes != "" {
		podMeta["notes"] = c.notes
	}
	if c.cod.Reference != "" {
		podMeta["collection_reference"] = c.cod.Reference
	}

	taskMeta := map[string]any{
		"delivered_at":  stamp,
		"delivered_via": c.via,
		"confirmed_by":  c.actor.Type,
	}
	for k, v := range c.taskMeta {
		taskMeta[k] = v
	}
	if c.recipient != "" {
		taskMeta["recipient_name"] = c.recipient
	}

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("tasks: begin: %w", err)
	}
	builder := tx.ProofOfDelivery.Create().
		SetTenantID(t.TenantID).
		SetTaskID(t.ID).
		SetFleetMemberID(memberID).
		SetCapturedAt(now).
		SetAmountCollected(c.cod.Amount).
		SetMetadata(podMeta)
	if c.photo != "" {
		builder.SetPhotoURL(c.photo)
	}
	if c.signature != "" {
		builder.SetSignatureURL(c.signature)
	}
	if c.otp != "" {
		builder.SetOtpCode(c.otp)
	}
	if c.cod.Method != "" {
		builder.SetCollectionMethod(c.cod.Method)
	}
	pod, err := builder.Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		if ent.IsConstraintError(err) {
			return nil, ErrProofExists
		}
		return nil, fmt.Errorf("tasks: create pod: %w", err)
	}
	upd := tx.Task.Update().
		Where(task.ID(t.ID), task.TenantID(t.TenantID), task.StatusEQ(t.Status)).
		SetStatus("delivered").
		SetMetadata(mergeMeta(t.Metadata, taskMeta))
	if c.cod.collected() {
		upd.SetCashCollected(true)
	}
	n, err := upd.Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("tasks: mark delivered: %w", err)
	}
	if n == 0 {
		_ = tx.Rollback()
		return nil, ErrTaskChanged
	}
	if _, err := tx.TaskAssignment.Update().
		Where(taskassignment.TaskID(t.ID), taskassignment.StatusIn(activeAssignmentStatuses...)).
		SetStatus("completed").
		SetCompletedAt(now).
		Save(ctx); err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("tasks: end assignment: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("tasks: commit delivery: %w", err)
	}

	history := map[string]any{
		"previous_status": t.Status,
		"delivered_via":   c.via,
		"fleet_member_id": memberID.String(),
	}
	if c.recipient != "" {
		history["recipient_name"] = c.recipient
	}
	if c.override != "" {
		history["override"] = true
		history["reason"] = c.override
	}
	if c.cod.Method != "" {
		history["collection_method"] = c.cod.Method
		history["amount_collected"] = c.cod.Amount
	}
	s.recordEvent(ctx, t.ID, "delivered", c.actor, history)

	s.log.Info("delivery completed",
		zap.String("task_id", t.ID.String()),
		zap.String("pod_id", pod.ID.String()),
		zap.String("via", c.via),
		zap.String("by", c.actor.Type))

	if s.publisher != nil {
		_ = s.publisher.PublishTaskCompleted(ctx, t.TenantID, events.TaskEventData{
			TaskID:              t.ID.String(),
			TrackingCode:        t.TrackingCode,
			ExternalReference:   t.ExternalReference,
			Status:              "delivered",
			PreviousStatus:      t.Status,
			FleetMemberID:       memberID.String(),
			SourceService:       t.SourceService,
			CashOnDelivery:      c.cod.Due,
			CashCollected:       c.cod.collected(),
			AmountCollected:     c.cod.Amount,
			OrderNumber:         metadataString(t.Metadata, "order_number"),
			CollectionMethod:    c.cod.Method,
			CollectionReference: c.cod.Reference,
			DeliveredVia:        c.via,
			ConfirmedBy:         c.actor.Type,
			RecipientName:       c.recipient,
			ProofPhoto:          c.photo != "",
			OverrideReason:      c.override,
		})
	}
	if s.sseBroadcaster != nil {
		s.sseBroadcaster.Publish(t.TenantID, t.ID, "status_changed", map[string]any{
			"task_id": t.ID, "status": "delivered", "previous_status": t.Status,
		})
	}
	if c.actor.Type != "rider" {
		s.notifyRider(ctx, t.TenantID, memberID, t.ID, "job_delivered",
			"Delivery confirmed", fmt.Sprintf("Order %s was marked delivered by the dispatcher.", orderLabel(t)))
	}
	s.recordDeliveryEarning(t, memberID)
	return pod, nil
}

// recordDeliveryEarning records the rider's earning (or staff per diem) in the background.
func (s *Service) recordDeliveryEarning(t *ent.Task, memberID uuid.UUID) {
	if s.earningsSvc == nil {
		return
	}
	// Prefer the task's actual delivery fee (set by ordering-backend in the task metadata) as
	// the rider earning. Fall back to the distance-based PricingRule path for tasks without one.
	var deliveryFee float64
	if t.Metadata != nil {
		if raw, ok := t.Metadata["delivery_fee"]; ok {
			deliveryFee = metadataNumber(raw)
		}
	}
	tenantID, taskID := t.TenantID, t.ID
	distanceKm := metadataNumber(t.Metadata["distance_km"])
	go func() {
		earnCtx := context.Background()
		// Staff riders are salaried on erp-api payroll: no per-task earning unless the member's
		// employment terms say otherwise.
		if m, merr := s.client.FleetMember.Get(earnCtx, memberID); merr == nil {
			emp := fleetmod.EmploymentOf(m)
			if emp.IsStaff() && s.perDiem != nil {
				// erp-api decides whether the trip earns per diem (distance, policy).
				s.perDiem(earnCtx, tenantID, taskID, memberID)
			}
			if !emp.EarnsPerTask() {
				return
			}
		}
		if deliveryFee > 0 {
			if err := s.earningsSvc.RecordEarningWithAmount(earnCtx, tenantID, taskID, memberID, deliveryFee); err != nil {
				s.log.Warn("failed to record delivery earning", zap.Error(err))
			}
			return
		}
		// Trip distance recorded at intake (road distance when routing was up).
		if err := s.earningsSvc.RecordEarning(earnCtx, tenantID, taskID, memberID, distanceKm); err != nil {
			s.log.Warn("failed to record delivery earning", zap.Error(err))
		}
	}()
}
