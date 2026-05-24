package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/earningsstatement"
	"github.com/bengobox/logistics-service/internal/ent/fleetmember"
	"github.com/bengobox/logistics-service/internal/modules/earnings"
)

// updatePayoutMethodRequest is the body for PUT /{tenant}/riders/{riderID}/payout-method.
type updatePayoutMethodRequest struct {
	PayoutMethod  string `json:"payout_method"`
	PayoutPhone   string `json:"payout_phone,omitempty"`
	PayoutBankCode string `json:"payout_bank_code,omitempty"`
	AccountNumber string `json:"account_number,omitempty"`
	AccountName   string `json:"account_name,omitempty"`
}

// maskAccountNumber masks all but the last 4 characters.
func maskAccountNumber(s string) string {
	if len(s) <= 4 {
		return s
	}
	return strings.Repeat("*", len(s)-4) + s[len(s)-4:]
}

// payoutMethodResponse is the masked payout method response.
type payoutMethodResponse struct {
	ID                  uuid.UUID `json:"id"`
	PayoutMethod        string    `json:"payout_method"`
	PayoutPhone         string    `json:"payout_phone,omitempty"`
	PayoutBankCode      string    `json:"payout_bank_code,omitempty"`
	PayoutAccountNumber string    `json:"payout_account_number,omitempty"`
	PayoutAccountName   string    `json:"payout_account_name,omitempty"`
	PayoutStatus        string    `json:"payout_status"`
}

func memberToPayoutResponse(m *ent.FleetMember) payoutMethodResponse {
	return payoutMethodResponse{
		ID:                  m.ID,
		PayoutMethod:        string(m.PayoutMethod),
		PayoutPhone:         m.PayoutPhone,
		PayoutBankCode:      m.PayoutBankCode,
		PayoutAccountNumber: maskAccountNumber(m.PayoutAccountNumber),
		PayoutAccountName:   m.PayoutAccountName,
		PayoutStatus:        string(m.PayoutStatus),
	}
}

// GetRiderPayoutMethod handles GET /{tenant}/riders/{riderID}/payout-method
func (h *EarningsHandler) GetRiderPayoutMethod(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	riderID, err := uuid.Parse(chi.URLParam(r, "riderID"))
	if err != nil {
		http.Error(w, "invalid riderID", http.StatusBadRequest)
		return
	}

	member, err := h.client.FleetMember.Query().
		Where(fleetmember.ID(riderID), fleetmember.TenantID(tenantID)).
		Only(r.Context())
	if err != nil {
		if ent.IsNotFound(err) {
			http.Error(w, "rider not found", http.StatusNotFound)
			return
		}
		h.log.Error("get payout method", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	respondJSON(w, http.StatusOK, memberToPayoutResponse(member))
}

// UpdateRiderPayoutMethod handles PUT /{tenant}/riders/{riderID}/payout-method
func (h *EarningsHandler) UpdateRiderPayoutMethod(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	riderID, err := uuid.Parse(chi.URLParam(r, "riderID"))
	if err != nil {
		http.Error(w, "invalid riderID", http.StatusBadRequest)
		return
	}

	var req updatePayoutMethodRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.PayoutMethod == "" {
		http.Error(w, "payout_method is required", http.StatusBadRequest)
		return
	}

	member, err := h.client.FleetMember.Query().
		Where(fleetmember.ID(riderID), fleetmember.TenantID(tenantID)).
		Only(r.Context())
	if err != nil {
		if ent.IsNotFound(err) {
			http.Error(w, "rider not found", http.StatusNotFound)
			return
		}
		h.log.Error("update payout method: query", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	upd := h.client.FleetMember.UpdateOne(member).
		SetPayoutMethod(fleetmember.PayoutMethod(req.PayoutMethod))
	if req.PayoutPhone != "" {
		upd = upd.SetPayoutPhone(req.PayoutPhone)
	}
	if req.PayoutBankCode != "" {
		upd = upd.SetPayoutBankCode(req.PayoutBankCode)
	}
	if req.AccountNumber != "" {
		upd = upd.SetPayoutAccountNumber(req.AccountNumber)
	}
	if req.AccountName != "" {
		upd = upd.SetPayoutAccountName(req.AccountName)
	}
	// Clear cached recipient code when payout details change (forces treasury to re-create it)
	upd = upd.ClearPayoutRecipientCode()

	updated, err := upd.Save(r.Context())
	if err != nil {
		h.log.Error("update payout method: save", zap.Error(err))
		http.Error(w, "failed to update payout method", http.StatusInternalServerError)
		return
	}

	respondJSON(w, http.StatusOK, memberToPayoutResponse(updated))
}

// SettleEarningsStatement handles POST /{tenant}/earnings/statements/{statementID}/settle
func (h *EarningsHandler) SettleEarningsStatement(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	statementID, err := uuid.Parse(chi.URLParam(r, "statementID"))
	if err != nil {
		http.Error(w, "invalid statementID", http.StatusBadRequest)
		return
	}

	if h.treasuryClient == nil {
		http.Error(w, "treasury client not configured", http.StatusInternalServerError)
		return
	}

	// Load statement
	stmt, err := h.client.EarningsStatement.Query().
		Where(
			earningsstatement.ID(statementID),
			earningsstatement.TenantID(tenantID),
		).Only(r.Context())
	if err != nil {
		if ent.IsNotFound(err) {
			http.Error(w, "statement not found", http.StatusNotFound)
			return
		}
		h.log.Error("settle statement: query", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if stmt.Status != "draft" {
		http.Error(w, fmt.Sprintf("statement is %s; only draft statements can be settled", stmt.Status), http.StatusConflict)
		return
	}

	// Load rider and validate payout_status
	member, err := h.client.FleetMember.Query().
		Where(fleetmember.ID(stmt.FleetMemberID), fleetmember.TenantID(tenantID)).
		WithUser().
		Only(r.Context())
	if err != nil {
		if ent.IsNotFound(err) {
			http.Error(w, "rider not found", http.StatusNotFound)
			return
		}
		h.log.Error("settle statement: query member", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if member.PayoutStatus == fleetmember.PayoutStatusSuspended {
		http.Error(w, "rider payout is suspended", http.StatusForbidden)
		return
	}

	// Resolve full name from user edge
	fullName := ""
	if member.Edges.User != nil {
		fullName = member.Edges.User.FullName
	}

	// Tenant slug from URL for treasury routing
	tenantSlug := chi.URLParam(r, "tenant")

	period := fmt.Sprintf("%s – %s",
		stmt.PeriodStart.Format("2 Jan"),
		stmt.PeriodEnd.Format("2 Jan 2006"),
	)

	disburseReq := earnings.DisburseRequest{
		EntityType:  "rider",
		EntityID:    member.ID.String(),
		Amount:      stmt.NetAmount,
		Currency:    "KES",
		Reference:   fmt.Sprintf("RIDER-%s", stmt.ID.String()),
		Reason:      fmt.Sprintf("Delivery earnings %s", period),
		PayoutMethod: string(member.PayoutMethod),
		Recipient: earnings.DisburseRecipient{
			Name:          fullName,
			Phone:         member.PayoutPhone,
			BankCode:      member.PayoutBankCode,
			AccountNumber: member.PayoutAccountNumber,
			RecipientCode: member.PayoutRecipientCode,
		},
	}

	disbResp, err := h.treasuryClient.DisbursePayout(r.Context(), tenantSlug, disburseReq)
	if err != nil {
		h.log.Error("settle statement: treasury disburse failed",
			zap.String("statement_id", statementID.String()),
			zap.Error(err))
		http.Error(w, "payout disbursement failed", http.StatusBadGateway)
		return
	}

	// Update statement to processing
	_, updateErr := h.client.EarningsStatement.UpdateOne(stmt).
		SetStatus("processing").
		SetMetadata(map[string]any{
			"payout_reference": disbResp.Reference,
			"payout_id":        disbResp.PayoutID,
			"settled_at":       time.Now().Format(time.RFC3339),
		}).
		Save(r.Context())
	if updateErr != nil {
		// Payout was submitted — log but don't fail the response
		h.log.Error("settle statement: update status failed",
			zap.String("statement_id", statementID.String()),
			zap.Error(updateErr))
	}

	h.log.Info("statement settlement initiated",
		zap.String("statement_id", statementID.String()),
		zap.String("payout_reference", disbResp.Reference),
	)

	respondJSON(w, http.StatusOK, map[string]any{
		"statement_id":     statementID,
		"status":           "processing",
		"payout_reference": disbResp.Reference,
	})
}
