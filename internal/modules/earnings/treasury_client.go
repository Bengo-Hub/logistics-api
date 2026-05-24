// Package earnings — treasury S2S client for rider payout disbursement.
// All calls use the shared INTERNAL_SERVICE_KEY via X-API-Key header.
package earnings

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// TreasuryClient is a thin S2S client for treasury-api payout operations.
type TreasuryClient struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// NewTreasuryClient creates a treasury S2S client for payout disbursement.
func NewTreasuryClient(baseURL, apiKey string) *TreasuryClient {
	return &TreasuryClient{
		baseURL: baseURL,
		apiKey:  apiKey,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// DisburseRecipient holds recipient details for a payout.
type DisburseRecipient struct {
	Name          string `json:"name"`
	Phone         string `json:"phone,omitempty"`
	BankCode      string `json:"bank_code,omitempty"`
	AccountNumber string `json:"account_number,omitempty"`
	RecipientCode string `json:"recipient_code,omitempty"`
}

// DisburseRequest is the body for POST /api/v1/{tenantSlug}/payouts/disburse.
type DisburseRequest struct {
	EntityType    string            `json:"entity_type"`   // "rider"
	EntityID      string            `json:"entity_id"`     // fleet_member UUID
	Amount        float64           `json:"amount"`
	Currency      string            `json:"currency"`
	Reference     string            `json:"reference"`     // e.g. "RIDER-{statement_id}"
	Reason        string            `json:"reason"`
	PayoutMethod  string            `json:"payout_method"`
	Recipient     DisburseRecipient `json:"recipient"`
}

// DisburseResponse is the response from POST /api/v1/{tenantSlug}/payouts/disburse.
type DisburseResponse struct {
	PayoutID  string `json:"payout_id"`
	Reference string `json:"reference"`
	Status    string `json:"status"`
}

// DisbursePayout calls POST /api/v1/{tenantSlug}/payouts/disburse on treasury-api.
func (c *TreasuryClient) DisbursePayout(ctx context.Context, tenantSlug string, req DisburseRequest) (*DisburseResponse, error) {
	url := fmt.Sprintf("%s/api/v1/%s/payouts/disburse", c.baseURL, tenantSlug)

	b, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("treasury: marshal disburse request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("treasury: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-API-Key", c.apiKey)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("treasury: request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("treasury: read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("treasury: upstream error %d: %s", resp.StatusCode, string(respBody))
	}

	var result DisburseResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("treasury: decode response: %w", err)
	}
	return &result, nil
}
