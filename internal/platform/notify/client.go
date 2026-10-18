// Package notify sends one direct message through notifications-api. Rider lifecycle emails go
// through logistics.fleet.* events; this client is only for messages that must not sit in the
// event outbox, such as an internal rider's account email carrying a temporary password.
//
// Plain net/http with no retries: a credentials email is sent once (notifications-api also
// dedupes on Idempotency-Key).
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Client calls notifications-api with the shared INTERNAL_SERVICE_KEY.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// NewClient builds the client. Sends fail fast when baseURL or apiKey is empty.
func NewClient(baseURL, apiKey string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, http: &http.Client{Timeout: timeout}}
}

// Enabled reports whether the client is configured.
func (c *Client) Enabled() bool { return c != nil && c.baseURL != "" && c.apiKey != "" }

// Message mirrors notifications-api's CreateMessageRequest.
type Message struct {
	Channel  string         `json:"channel"`
	Tenant   string         `json:"tenant"`
	Template string         `json:"template"`
	Data     map[string]any `json:"data"`
	To       []string       `json:"to"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// Send queues one message for the tenant. idem makes a repeated call a no-op.
func (c *Client) Send(ctx context.Context, tenantID uuid.UUID, tenantSlug, idem string, m Message) error {
	if !c.Enabled() {
		return errors.New("notify: client not configured (NOTIFICATIONS_SERVICE_URL / INTERNAL_SERVICE_KEY)")
	}
	m.Tenant = tenantSlug
	if m.Metadata == nil {
		m.Metadata = map[string]any{}
	}
	m.Metadata["source_service"] = "logistics"
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/api/v1/"+tenantSlug+"/notifications/messages", bytes.NewReader(body))
	if err != nil {
		return err
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("X-API-Key", c.apiKey)
	hr.Header.Set("X-Tenant-ID", tenantID.String())
	if idem != "" {
		hr.Header.Set("Idempotency-Key", idem)
	}
	resp, err := c.http.Do(hr)
	if err != nil {
		return fmt.Errorf("notify: send: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("notify: send: status %d", resp.StatusCode)
	}
	return nil
}
