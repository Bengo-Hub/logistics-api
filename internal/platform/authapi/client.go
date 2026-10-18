// Package authapi is logistics-api's client for auth-api, which owns every account. logistics
// uses it to give an internal rider an SSO account when the admin onboards them: auth-api's S2S
// member endpoint finds the user by email or creates one with a temporary password that must
// be changed on first sign-in (profile.must_change_password).
//
// Plain net/http on purpose, no retries: a retried create answers "existing user" without the
// one-time temporary password, so a retry would lose it.
package authapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

// RoleDriver is the SSO role riders hold; logistics RBAC maps it to the driver system role.
const RoleDriver = "driver"

// Client calls auth-api with the shared INTERNAL_SERVICE_KEY.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// NewClient builds the client. Calls fail fast when baseURL or apiKey is empty.
func NewClient(baseURL, apiKey string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, http: &http.Client{Timeout: timeout}}
}

// Enabled reports whether the client is configured.
func (c *Client) Enabled() bool { return c != nil && c.baseURL != "" && c.apiKey != "" }

// MemberRequest mirrors auth-api's addTenantMemberRequest (admin_handler.go).
type MemberRequest struct {
	Email string   `json:"email"`
	Name  string   `json:"name,omitempty"`
	Phone string   `json:"phone,omitempty"`
	Roles []string `json:"roles"`
	// MergeRoles adds the rider role to an existing member instead of replacing their roles.
	MergeRoles bool `json:"merge_roles,omitempty"`
	// SkipWelcome stops auth-api's generic welcome: logistics sends its own rider email.
	SkipWelcome bool `json:"skip_welcome,omitempty"`
}

// Member is the part of auth-api's tenantMemberResponse logistics needs.
type Member struct {
	UserID uuid.UUID `json:"user_id"`
	// TempPassword is set ONCE, only when this call created the account. Never log or store it.
	TempPassword string `json:"temp_password,omitempty"`
}

// AddMember finds or creates the auth account for req.Email and makes it a member of the
// tenant with req.Roles.
func (c *Client) AddMember(ctx context.Context, tenantID uuid.UUID, req MemberRequest) (*Member, error) {
	if !c.Enabled() {
		return nil, errors.New("authapi: client not configured (AUTH_SERVICE_URL / INTERNAL_SERVICE_KEY)")
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/api/v1/s2s/tenants/"+tenantID.String()+"/members", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("X-API-Key", c.apiKey)
	resp, err := c.http.Do(hr)
	if err != nil {
		return nil, fmt.Errorf("authapi: add member: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("authapi: add member: status %d: %s", resp.StatusCode, errorMessage(raw))
	}
	var m Member
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("authapi: decode member: %w", err)
	}
	if m.UserID == uuid.Nil {
		return nil, errors.New("authapi: add member: no user id in response")
	}
	return &m, nil
}

// errorMessage keeps only auth-api's own message or error field: raw bodies can echo the email
// sent, and these errors reach logs and API responses.
func errorMessage(body []byte) string {
	var b struct {
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	msg := "unexpected response"
	if json.Unmarshal(body, &b) == nil {
		if b.Message != "" {
			msg = b.Message
		} else if b.Error != "" {
			msg = b.Error
		}
	}
	if r := []rune(msg); len(r) > 160 {
		msg = string(r[:160])
	}
	return msg
}
