package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	authclient "github.com/Bengo-Hub/shared-auth-client"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/billingevent"
	"github.com/bengobox/logistics-service/internal/ent/earningsstatement"
	"github.com/bengobox/logistics-service/internal/ent/fleetmember"
	"github.com/bengobox/logistics-service/internal/modules/earnings"
)

// EarningsHandler exposes earnings, billing event, and pricing rule endpoints.
type EarningsHandler struct {
	log            *zap.Logger
	client         *ent.Client
	earningsSvc    *earnings.Service
	treasuryClient *earnings.TreasuryClient
	// pricingGate guards rider pricing-rule writes (logistics.pricing.manage).
	pricingGate func(http.Handler) http.Handler
}

// SetPricingGate sets the middleware that guards pricing-rule mutations.
func (h *EarningsHandler) SetPricingGate(mw func(http.Handler) http.Handler) { h.pricingGate = mw }

// NewEarningsHandler creates a new EarningsHandler.
func NewEarningsHandler(log *zap.Logger, client *ent.Client, earningsSvc *earnings.Service) *EarningsHandler {
	return &EarningsHandler{
		log:         log.Named("earnings.handler"),
		client:      client,
		earningsSvc: earningsSvc,
	}
}

// SetTreasuryClient injects the treasury S2S client for payout disbursement.
func (h *EarningsHandler) SetTreasuryClient(tc *earnings.TreasuryClient) {
	h.treasuryClient = tc
}

// RegisterRoutes wires all earnings sub-routes onto the given router (already scoped to /{tenant}).
func (h *EarningsHandler) RegisterRoutes(r chi.Router) {
	r.Route("/earnings", func(e chi.Router) {
		e.Get("/statements", h.ListStatements)
		e.Get("/statements/{statementId}", h.GetStatement)
		e.Post("/statements/generate", h.GenerateStatements)
		e.Get("/events", h.ListBillingEvents)
		e.Get("/pricing-rules", h.ListPricingRules)
		e.Group(func(pr chi.Router) {
			if h.pricingGate != nil {
				pr.Use(h.pricingGate)
			}
			pr.Post("/pricing-rules", h.CreatePricingRule)
			pr.Patch("/pricing-rules/{ruleId}", h.UpdatePricingRule)
			pr.Delete("/pricing-rules/{ruleId}", h.DeletePricingRule)
		})
		// Statement settlement → rider payout disbursement via treasury-api
		e.Post("/statements/{statementID}/settle", h.SettleEarningsStatement)
	})

	// Rider self-service earnings
	r.Get("/riders/me/earnings", h.GetMyEarnings)
	r.Get("/riders/me/earnings/events", h.ListMyBillingEvents)
	r.Get("/riders/me/earnings/statements", h.ListMyStatements)

	// Rider payout method management
	r.Get("/riders/{riderID}/payout-method", h.GetRiderPayoutMethod)
	r.Put("/riders/{riderID}/payout-method", h.UpdateRiderPayoutMethod)
}

// ListStatements handles GET /api/v1/{tenant}/earnings/statements
// Query params: member_id, status, from, to
func (h *EarningsHandler) ListStatements(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	q := h.client.EarningsStatement.Query().
		Where(earningsstatement.TenantID(tenantID)).
		Order(ent.Desc(earningsstatement.FieldPeriodStart))

	if memberIDStr := r.URL.Query().Get("member_id"); memberIDStr != "" {
		if memberID, err := uuid.Parse(memberIDStr); err == nil {
			q = q.Where(earningsstatement.FleetMemberID(memberID))
		}
	}
	if status := r.URL.Query().Get("status"); status != "" {
		q = q.Where(earningsstatement.Status(status))
	}
	if fromStr := r.URL.Query().Get("from"); fromStr != "" {
		if t, err := time.Parse(time.RFC3339, fromStr); err == nil {
			q = q.Where(earningsstatement.PeriodStartGTE(t))
		}
	}
	if toStr := r.URL.Query().Get("to"); toStr != "" {
		if t, err := time.Parse(time.RFC3339, toStr); err == nil {
			q = q.Where(earningsstatement.PeriodEndLTE(t))
		}
	}

	stmts, err := q.Limit(100).All(r.Context())
	if err != nil {
		h.log.Error("list statements", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, stmts)
}

// GetStatement handles GET /api/v1/{tenant}/earnings/statements/{statementId}
func (h *EarningsHandler) GetStatement(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	stmtID, err := uuid.Parse(chi.URLParam(r, "statementId"))
	if err != nil {
		http.Error(w, "invalid statementId", http.StatusBadRequest)
		return
	}

	stmt, err := h.client.EarningsStatement.Query().
		Where(
			earningsstatement.ID(stmtID),
			earningsstatement.TenantID(tenantID),
		).Only(r.Context())
	if err != nil {
		if ent.IsNotFound(err) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		h.log.Error("get statement", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, stmt)
}

// GenerateStatements handles POST /api/v1/{tenant}/earnings/statements/generate
// Optional ?date=YYYY-MM-DD (UTC day) selects the statement day; default is yesterday.
// Members that already have a statement for that day are skipped.
func (h *EarningsHandler) GenerateStatements(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	day := time.Now().UTC().Add(-24 * time.Hour)
	if v := r.URL.Query().Get("date"); v != "" {
		parsed, err := time.Parse("2006-01-02", v)
		if err != nil {
			http.Error(w, "date must be YYYY-MM-DD", http.StatusBadRequest)
			return
		}
		day = parsed
	}
	if err := h.earningsSvc.GenerateStatements(r.Context(), tenantID, day); err != nil {
		h.log.Error("generate statements", zap.Error(err))
		http.Error(w, "failed to generate statements", http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ListBillingEvents handles GET /api/v1/{tenant}/earnings/events
// Query params: task_id, from, to
func (h *EarningsHandler) ListBillingEvents(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	q := h.client.BillingEvent.Query().
		Where(billingevent.TenantID(tenantID)).
		Order(ent.Desc(billingevent.FieldOccurredAt))

	if taskIDStr := r.URL.Query().Get("task_id"); taskIDStr != "" {
		if taskID, err := uuid.Parse(taskIDStr); err == nil {
			q = q.Where(billingevent.TaskID(taskID))
		}
	}
	if fromStr := r.URL.Query().Get("from"); fromStr != "" {
		if t, err := time.Parse(time.RFC3339, fromStr); err == nil {
			q = q.Where(billingevent.OccurredAtGTE(t))
		}
	}
	if toStr := r.URL.Query().Get("to"); toStr != "" {
		if t, err := time.Parse(time.RFC3339, toStr); err == nil {
			q = q.Where(billingevent.OccurredAtLTE(t))
		}
	}

	events, err := q.Limit(200).All(r.Context())
	if err != nil {
		h.log.Error("list billing events", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, events)
}

// ListPricingRules handles GET /api/v1/{tenant}/earnings/pricing-rules
func (h *EarningsHandler) ListPricingRules(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	rules, err := h.earningsSvc.ListRules(r.Context(), tenantID)
	if err != nil {
		h.log.Error("list pricing rules", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, rules)
}

// CreatePricingRule handles POST /api/v1/{tenant}/earnings/pricing-rules
func (h *EarningsHandler) CreatePricingRule(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req earnings.RuleInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	rule, err := h.earningsSvc.CreateRule(r.Context(), tenantID, req)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	respondJSON(w, http.StatusCreated, rule)
}

// UpdatePricingRule handles PATCH /api/v1/{tenant}/earnings/pricing-rules/{ruleId}
func (h *EarningsHandler) UpdatePricingRule(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	ruleID, err := uuid.Parse(chi.URLParam(r, "ruleId"))
	if err != nil {
		http.Error(w, "invalid ruleId", http.StatusBadRequest)
		return
	}

	var req earnings.RulePatch
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	rule, err := h.earningsSvc.UpdateRule(r.Context(), tenantID, ruleID, req)
	if err != nil {
		if errors.Is(err, earnings.ErrRuleNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	respondJSON(w, http.StatusOK, rule)
}

// DeletePricingRule handles DELETE /api/v1/{tenant}/earnings/pricing-rules/{ruleId}
func (h *EarningsHandler) DeletePricingRule(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	ruleID, err := uuid.Parse(chi.URLParam(r, "ruleId"))
	if err != nil {
		http.Error(w, "invalid ruleId", http.StatusBadRequest)
		return
	}

	if err := h.earningsSvc.DeleteRule(r.Context(), tenantID, ruleID); err != nil {
		if errors.Is(err, earnings.ErrRuleNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		h.log.Error("delete pricing rule", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GetMyEarnings handles GET /api/v1/{tenant}/riders/me/earnings
// Returns today/week/month earnings totals for the authenticated rider.
func (h *EarningsHandler) GetMyEarnings(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Resolve fleet member from the JWT subject (auth user ID)
	claims, ok := authclient.ClaimsFromContext(r.Context())
	if !ok || claims.Subject == "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	authUserID, err := uuid.Parse(claims.Subject)
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	member, err := h.client.FleetMember.Query().
		Where(
			fleetmember.UserID(authUserID),
			fleetmember.TenantID(tenantID),
		).Only(r.Context())
	if err != nil {
		if ent.IsNotFound(err) {
			http.Error(w, "rider not found in fleet", http.StatusNotFound)
			return
		}
		h.log.Error("resolve fleet member", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	now := time.Now().UTC()
	todayStart := now.Truncate(24 * time.Hour)
	weekStart := todayStart.AddDate(0, 0, -int(todayStart.Weekday()))
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)

	today, week, month, err := h.earningsSvc.RiderTotals(r.Context(), tenantID, member.ID, todayStart, weekStart, monthStart)
	if err != nil {
		h.log.Error("rider earnings totals", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"member_id": member.ID,
		"today":     today,
		"week":      week,
		"month":     month,
		"currency":  "KES",
	})
}

// ListMyStatements handles GET /api/v1/{tenant}/riders/me/earnings/statements
func (h *EarningsHandler) ListMyStatements(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	claims, ok := authclient.ClaimsFromContext(r.Context())
	if !ok || claims.Subject == "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	authUserID, err := uuid.Parse(claims.Subject)
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	member, err := h.client.FleetMember.Query().
		Where(
			fleetmember.UserID(authUserID),
			fleetmember.TenantID(tenantID),
		).Only(r.Context())
	if err != nil {
		if ent.IsNotFound(err) {
			http.Error(w, "rider not found in fleet", http.StatusNotFound)
			return
		}
		h.log.Error("resolve fleet member for statements", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	stmts, err := h.client.EarningsStatement.Query().
		Where(
			earningsstatement.TenantID(tenantID),
			earningsstatement.FleetMemberID(member.ID),
		).
		Order(ent.Desc(earningsstatement.FieldPeriodStart)).
		Limit(50).
		All(r.Context())
	if err != nil {
		h.log.Error("list my statements", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, stmts)
}

// ListMyBillingEvents handles GET /api/v1/{tenant}/riders/me/earnings/events
// Returns billing events scoped to the authenticated rider only.
// Query params: task_id, from, to
func (h *EarningsHandler) ListMyBillingEvents(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Resolve fleet member from the JWT subject (auth user ID)
	claims, ok := authclient.ClaimsFromContext(r.Context())
	if !ok || claims.Subject == "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	authUserID, err := uuid.Parse(claims.Subject)
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	member, err := h.client.FleetMember.Query().
		Where(
			fleetmember.UserID(authUserID),
			fleetmember.TenantID(tenantID),
		).Only(r.Context())
	if err != nil {
		if ent.IsNotFound(err) {
			http.Error(w, "rider not found in fleet", http.StatusNotFound)
			return
		}
		h.log.Error("resolve fleet member for billing events", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	q := h.client.BillingEvent.Query().
		Where(billingevent.TenantID(tenantID), earnings.ForMember(member.ID)).
		Order(ent.Desc(billingevent.FieldOccurredAt))

	if taskIDStr := r.URL.Query().Get("task_id"); taskIDStr != "" {
		if taskID, err := uuid.Parse(taskIDStr); err == nil {
			q = q.Where(billingevent.TaskID(taskID))
		}
	}
	if fromStr := r.URL.Query().Get("from"); fromStr != "" {
		if t, err := time.Parse(time.RFC3339, fromStr); err == nil {
			q = q.Where(billingevent.OccurredAtGTE(t))
		}
	}
	if toStr := r.URL.Query().Get("to"); toStr != "" {
		if t, err := time.Parse(time.RFC3339, toStr); err == nil {
			q = q.Where(billingevent.OccurredAtLTE(t))
		}
	}

	events, err := q.Limit(200).All(r.Context())
	if err != nil {
		h.log.Error("list my billing events", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	respondJSON(w, http.StatusOK, events)
}
