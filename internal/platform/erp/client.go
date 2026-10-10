// Package erp is a thin S2S client for erp-api, which owns HR and payroll. logistics uses it
// only to raise per diem claims for staff riders; rates, eligibility, tax and approval stay
// in erp-api.
package erp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// Client calls erp-api with the shared INTERNAL_SERVICE_KEY.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// NewClient builds the client. Calls fail fast when baseURL or apiKey is empty.
func NewClient(baseURL, apiKey string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Client{baseURL: baseURL, apiKey: apiKey, http: &http.Client{Timeout: timeout}}
}

// Enabled reports whether the client is configured.
func (c *Client) Enabled() bool { return c != nil && c.baseURL != "" && c.apiKey != "" }

// ClaimRequest is erp-api's POST /hrm/claims/external body.
type ClaimRequest struct {
	AuthUserID  string         `json:"auth_user_id"`
	Source      string         `json:"source"`
	SourceKey   string         `json:"source_key"`
	ClaimType   string         `json:"claim_type"`
	Days        float64        `json:"days,omitempty"`
	DistanceKm  float64        `json:"distance_km,omitempty"`
	StartedAt   *time.Time     `json:"started_at,omitempty"`
	EndedAt     *time.Time     `json:"ended_at,omitempty"`
	Description string         `json:"description,omitempty"`
	Reference   map[string]any `json:"reference,omitempty"`
}

// Claim is the part of erp-api's claim view logistics shows.
type Claim struct {
	ID       string `json:"id"`
	Amount   string `json:"amount"`
	Status   string `json:"status"`
	Approved bool   `json:"approved"`
	IsPaid   bool   `json:"is_paid"`
}

// Rejection is an erp-api refusal that is not a fault: the rider is not an employee, per diem
// is not set up, or the trip is too short. Code is erp-api's error code.
type Rejection struct {
	Code    string
	Message string
}

func (r *Rejection) Error() string { return fmt.Sprintf("erp: %s: %s", r.Code, r.Message) }

// IsRejection reports whether err is an erp-api business refusal.
func IsRejection(err error) (*Rejection, bool) {
	var r *Rejection
	ok := errors.As(err, &r)
	return r, ok
}

// RaiseClaim raises (or returns the existing) claim for req.SourceKey.
func (c *Client) RaiseClaim(ctx context.Context, tenantID uuid.UUID, req ClaimRequest) (*Claim, error) {
	if !c.Enabled() {
		return nil, errors.New("erp client not configured")
	}
	buf, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v1/hrm/claims/external", bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-API-Key", c.apiKey)
	httpReq.Header.Set("X-Tenant-ID", tenantID.String())
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	switch {
	case resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated:
		var out Claim
		if err := json.Unmarshal(body, &out); err != nil {
			return nil, fmt.Errorf("erp: decode claim: %w", err)
		}
		return &out, nil
	case resp.StatusCode == http.StatusUnprocessableEntity:
		// erp-api errors are {"error": "<message>", "code": "<code>"}.
		var e struct {
			Error string `json:"error"`
			Code  string `json:"code"`
		}
		_ = json.Unmarshal(body, &e)
		return nil, &Rejection{Code: e.Code, Message: e.Error}
	default:
		return nil, fmt.Errorf("erp: POST claims/external returned HTTP %d: %s", resp.StatusCode, string(body[:min(len(body), 512)]))
	}
}
