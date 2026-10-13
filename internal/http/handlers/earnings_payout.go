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

	"github.com/Bengo-Hub/httpware/contact"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/earningsstatement"
	"github.com/bengobox/logistics-service/internal/ent/fleetmember"
	"github.com/bengobox/logistics-service/internal/modules/earnings"
	notifmod "github.com/bengobox/logistics-service/internal/modules/notifications"
)

// updatePayoutMethodRequest is the body for PUT /{tenant}/riders/{riderID}/payout-method.
type updatePayoutMethodRequest struct {
	PayoutMethod   string `json:"payout_method"`
	PayoutPhone    string `json:"payout_phone,omitempty"`
	PayoutBankCode string `json:"payout_bank_code,omitempty"`
	AccountNumber  string `json:"account_number,omitempty"`
	AccountName    string `json:"account_name,omitempty"`
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

// payoutMethods are the methods a rider can be paid by (fleetmember.payout_method).
var payoutMethods = map[string]bool{
	"mpesa_b2c": true, "paystack_bank": true, "paystack_mobile": true, "cash": true, "internal_wallet": true,
}

// validatePayout checks the request for its method and returns the phone in the 254... form
// treasury pays to.
func validatePayout(req updatePayoutMethodRequest) (phone string, msg string) {
	if !payoutMethods[req.PayoutMethod] {
		return "", "Choose how the rider is paid."
	}
	switch req.PayoutMethod {
	case "mpesa_b2c", "paystack_mobile":
		e164, err := contact.NormalizePhone(req.PayoutPhone, "KE")
		if err != nil {
			return "", "Enter the phone number payouts go to, for example 0712 345 678."
		}
		return strings.TrimPrefix(e164, "+"), ""
	case "paystack_bank":
		if strings.TrimSpace(req.PayoutBankCode) == "" || strings.TrimSpace(req.AccountNumber) == "" {
			return "", "Bank payouts need the bank and the account number."
		}
	}
	return "", ""
}

// payoutRider loads a rider of the tenant by id.
func (h *EarningsHandler) payoutRider(w http.ResponseWriter, r *http.Request) (*ent.FleetMember, bool) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return nil, false
	}
	riderID, err := uuid.Parse(chi.URLParam(r, "riderID"))
	if err != nil {
		http.Error(w, "invalid riderID", http.StatusBadRequest)
		return nil, false
	}
	member, err := h.client.FleetMember.Query().
		Where(fleetmember.ID(riderID), fleetmember.TenantID(tenantID)).
		Only(r.Context())
	if err != nil {
		if ent.IsNotFound(err) {
			http.Error(w, "rider not found", http.StatusNotFound)
			return nil, false
		}
		h.log.Error("load rider for payout method", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return nil, false
	}
	return member, true
}

// savePayoutMethod validates and stores the payout details of a rider.
func (h *EarningsHandler) savePayoutMethod(w http.ResponseWriter, r *http.Request, member *ent.FleetMember) {
	var req updatePayoutMethodRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	phone, msg := validatePayout(req)
	if msg != "" {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}

	upd := h.client.FleetMember.UpdateOne(member).
		SetPayoutMethod(fleetmember.PayoutMethod(req.PayoutMethod))
	if phone != "" {
		upd = upd.SetPayoutPhone(phone)
	}
	if req.PayoutBankCode != "" {
		upd = upd.SetPayoutBankCode(strings.TrimSpace(req.PayoutBankCode))
	}
	if req.AccountNumber != "" {
		upd = upd.SetPayoutAccountNumber(strings.TrimSpace(req.AccountNumber))
	}
	if req.AccountName != "" {
		upd = upd.SetPayoutAccountName(strings.TrimSpace(req.AccountName))
	}
	// New details need a new recipient in treasury.
	upd = upd.ClearPayoutRecipientCode()

	updated, err := upd.Save(r.Context())
	if err != nil {
		h.log.Error("update payout method: save", zap.Error(err))
		http.Error(w, "failed to update payout method", http.StatusInternalServerError)
		return
	}
	h.log.Info("payout method changed",
		zap.String("fleet_member_id", member.ID.String()), zap.String("method", req.PayoutMethod))
	respondJSON(w, http.StatusOK, memberToPayoutResponse(updated))
}

// GetRiderPayoutMethod handles GET /{tenant}/riders/{riderID}/payout-method (earnings managers).
func (h *EarningsHandler) GetRiderPayoutMethod(w http.ResponseWriter, r *http.Request) {
	member, ok := h.payoutRider(w, r)
	if !ok {
		return
	}
	respondJSON(w, http.StatusOK, memberToPayoutResponse(member))
}

// UpdateRiderPayoutMethod handles PUT /{tenant}/riders/{riderID}/payout-method (earnings managers).
func (h *EarningsHandler) UpdateRiderPayoutMethod(w http.ResponseWriter, r *http.Request) {
	member, ok := h.payoutRider(w, r)
	if !ok {
		return
	}
	h.savePayoutMethod(w, r, member)
}

// GetMyPayoutMethod handles GET /{tenant}/riders/me/payout-method
func (h *EarningsHandler) GetMyPayoutMethod(w http.ResponseWriter, r *http.Request) {
	_, member, ok := currentRiderOf(w, r, h.client, h.log)
	if !ok {
		return
	}
	respondJSON(w, http.StatusOK, memberToPayoutResponse(member))
}

// UpdateMyPayoutMethod handles PUT /{tenant}/riders/me/payout-method
func (h *EarningsHandler) UpdateMyPayoutMethod(w http.ResponseWriter, r *http.Request) {
	_, member, ok := currentRiderOf(w, r, h.client, h.log)
	if !ok {
		return
	}
	h.savePayoutMethod(w, r, member)
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

	period := fmt.Sprintf("%s to %s",
		stmt.PeriodStart.Format("2 Jan"),
		stmt.PeriodEnd.Format("2 Jan 2006"),
	)

	disburseReq := earnings.DisburseRequest{
		EntityType:   "rider",
		EntityID:     member.ID.String(),
		Amount:       stmt.NetAmount,
		Currency:     "KES",
		Reference:    fmt.Sprintf("RIDER-%s", stmt.ID.String()),
		Reason:       fmt.Sprintf("Delivery earnings %s", period),
		PayoutMethod: string(member.PayoutMethod),
		Recipient: earnings.DisburseRecipient{
			Name:          fullName,
			Phone:         member.PayoutPhone,
			BankCode:      member.PayoutBankCode,
			AccountNumber: member.PayoutAccountNumber,
			RecipientCode: member.PayoutRecipientCode,
		},
	}

	// Claim the statement before paying: only one request can move it out of draft, so a double
	// click or two managers settling at once cannot pay the rider twice.
	claimed, err := h.client.EarningsStatement.Update().
		Where(
			earningsstatement.ID(stmt.ID),
			earningsstatement.TenantID(tenantID),
			earningsstatement.Status("draft"),
		).
		SetStatus("processing").
		Save(r.Context())
	if err != nil {
		h.log.Error("settle statement: claim", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if claimed == 0 {
		http.Error(w, "this statement is already being paid", http.StatusConflict)
		return
	}

	disbResp, err := h.treasuryClient.DisbursePayout(r.Context(), tenantSlug, disburseReq)
	if err != nil {
		h.log.Error("settle statement: treasury disburse failed",
			zap.String("statement_id", statementID.String()),
			zap.Error(err))
		// Nothing was paid: hand the statement back so it can be settled again.
		if _, rerr := h.client.EarningsStatement.Update().
			Where(earningsstatement.ID(stmt.ID), earningsstatement.Status("processing")).
			SetStatus("draft").
			Save(r.Context()); rerr != nil {
			h.log.Error("settle statement: release claim", zap.Error(rerr))
		}
		http.Error(w, "payout disbursement failed", http.StatusBadGateway)
		return
	}

	// Record the payout reference on the statement, keeping what its metadata already holds.
	meta := map[string]any{}
	for k, v := range stmt.Metadata {
		meta[k] = v
	}
	meta["payout_reference"] = disbResp.Reference
	meta["payout_id"] = disbResp.PayoutID
	meta["settled_at"] = time.Now().Format(time.RFC3339)
	_, updateErr := h.client.EarningsStatement.UpdateOne(stmt).
		SetStatus("processing").
		SetMetadata(meta).
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
	if h.notif != nil {
		member := stmt.FleetMemberID
		if _, nerr := h.notif.Create(r.Context(), notifmod.CreateRequest{
			TenantID:          tenantID,
			NotificationType:  "payout_sent",
			Title:             "Payment sent",
			Body:              fmt.Sprintf("KES %.2f for %s is on its way. Reference %s.", stmt.NetAmount, period, disbResp.Reference),
			Payload:           map[string]any{"statement_id": stmt.ID.String(), "payout_reference": disbResp.Reference},
			RecipientMemberID: &member,
		}); nerr != nil {
			h.log.Warn("payout notice", zap.Error(nerr))
		}
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"statement_id":     statementID,
		"status":           "processing",
		"payout_reference": disbResp.Reference,
	})
}
