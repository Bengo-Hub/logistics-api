package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// memTicketStore is an in-memory TicketStore with Redis-like TTL and GETDEL semantics.
type memTicketStore struct {
	mu   sync.Mutex
	now  func() time.Time
	data map[string]memEntry
}

type memEntry struct {
	value []byte
	until time.Time
}

func newMemStore(now func() time.Time) *memTicketStore {
	return &memTicketStore{now: now, data: map[string]memEntry{}}
}

func (m *memTicketStore) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = memEntry{value: value, until: m.now().Add(ttl)}
	return nil
}

func (m *memTicketStore) GetDel(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.data[key]
	delete(m.data, key)
	if !ok || !m.now().Before(e.until) {
		return nil, ErrTicketInvalid
	}
	return e.value, nil
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newTickets(ttl time.Duration) (*StreamTickets, *memTicketStore, *clock) {
	c := &clock{t: time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)}
	store := newMemStore(c.now)
	t := NewStreamTickets(store, ttl)
	t.now = c.now
	return t, store, c
}

func testClaims(c *clock) *authclient.Claims {
	claims := &authclient.Claims{
		TenantID:             uuid.NewString(),
		TenantSlug:           "acme",
		Roles:                []string{"dispatcher"},
		SubscriptionFeatures: []string{"live_tracking"},
	}
	claims.Subject = uuid.NewString()
	claims.RegisteredClaims.ExpiresAt = jwt.NewNumericDate(c.t.Add(15 * time.Minute))
	return claims
}

func TestStreamTicketIssueAndConsumeOnce(t *testing.T) {
	tickets, store, c := newTickets(0)
	if tickets.TTL() != DefaultStreamTicketTTL {
		t.Fatalf("default ttl: got %s", tickets.TTL())
	}
	claims := testClaims(c)
	ticket, ttl, err := tickets.Issue(context.Background(), claims)
	if err != nil {
		t.Fatal(err)
	}
	if ttl != 30*time.Second {
		t.Fatalf("ttl: got %s", ttl)
	}
	if len(ticket) < 40 {
		t.Fatalf("ticket too short: %q", ticket)
	}
	// Only the hash is stored, never the ticket itself.
	for k := range store.data {
		if strings.Contains(k, ticket) {
			t.Fatal("raw ticket used as the store key")
		}
	}

	got, err := tickets.Consume(context.Background(), ticket)
	if err != nil {
		t.Fatalf("first use: %v", err)
	}
	if got.Subject != claims.Subject || got.TenantID != claims.TenantID || got.GetTenantSlug() != "acme" ||
		len(got.Roles) != 1 || got.Roles[0] != "dispatcher" || !got.HasFeature("live_tracking") {
		t.Fatalf("claims not carried over: %+v", got)
	}
	if _, err := tickets.Consume(context.Background(), ticket); err == nil {
		t.Fatal("reused ticket accepted")
	}
}

func TestStreamTicketExpires(t *testing.T) {
	tickets, _, c := newTickets(30 * time.Second)
	ticket, _, err := tickets.Issue(context.Background(), testClaims(c))
	if err != nil {
		t.Fatal(err)
	}
	c.t = c.t.Add(31 * time.Second)
	if _, err := tickets.Consume(context.Background(), ticket); err == nil {
		t.Fatal("expired ticket accepted")
	}
}

func TestStreamTicketNeverOutlivesToken(t *testing.T) {
	tickets, _, c := newTickets(30 * time.Second)
	claims := testClaims(c)
	claims.RegisteredClaims.ExpiresAt = jwt.NewNumericDate(c.t.Add(10 * time.Second))
	_, ttl, err := tickets.Issue(context.Background(), claims)
	if err != nil {
		t.Fatal(err)
	}
	if ttl != 10*time.Second {
		t.Fatalf("ttl should be capped by token expiry, got %s", ttl)
	}
	claims.RegisteredClaims.ExpiresAt = jwt.NewNumericDate(c.t.Add(-time.Second))
	if _, _, err := tickets.Issue(context.Background(), claims); err == nil {
		t.Fatal("ticket issued for an expired token")
	}
}

func TestStreamTicketUnknownRejected(t *testing.T) {
	tickets, _, _ := newTickets(0)
	if _, err := tickets.Consume(context.Background(), "made-up"); err == nil {
		t.Fatal("unknown ticket accepted")
	}
	if _, err := tickets.Consume(context.Background(), ""); err == nil {
		t.Fatal("empty ticket accepted")
	}
}

func TestIssueHandler(t *testing.T) {
	tickets, _, c := newTickets(0)

	rec := httptest.NewRecorder()
	tickets.IssueHandler(rec, httptest.NewRequest(http.MethodPost, "/api/v1/acme/stream-ticket", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no claims: got %d", rec.Code)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/acme/stream-ticket", nil)
	req = req.WithContext(authclient.ContextWithClaims(req.Context(), testClaims(c)))
	rec = httptest.NewRecorder()
	tickets.IssueHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("issue: got %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Ticket    string `json:"ticket"`
		ExpiresIn int    `json:"expires_in"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Ticket == "" || body.ExpiresIn != 30 {
		t.Fatalf("bad body: %s", rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("ticket response must not be cached")
	}
}

func runWS(tickets *StreamTickets, target string) (int, *authclient.Claims) {
	var seen *authclient.Claims
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	authed, ok := AuthenticateWS(nil, tickets, rec, req)
	if ok {
		seen, _ = authclient.ClaimsFromContext(authed.Context())
		return http.StatusOK, seen
	}
	return rec.Code, nil
}

func TestAuthenticateWSTicket(t *testing.T) {
	tickets, _, c := newTickets(0)
	claims := testClaims(c)
	ticket, _, err := tickets.Issue(context.Background(), claims)
	if err != nil {
		t.Fatal(err)
	}
	code, got := runWS(tickets, "/api/v1/acme/notifications/stream?ticket="+ticket)
	if code != http.StatusOK || got == nil || got.Subject != claims.Subject {
		t.Fatalf("valid ticket: got %d", code)
	}
	if code, _ := runWS(tickets, "/api/v1/acme/notifications/stream?ticket="+ticket); code != http.StatusUnauthorized {
		t.Fatalf("reused ticket: got %d", code)
	}
}

func TestAuthenticateWSRejectsTokenInQuery(t *testing.T) {
	tickets, _, _ := newTickets(0)
	// A JWT-shaped value in ?token= is not looked at at all.
	code, _ := runWS(tickets, "/api/v1/acme/notifications/stream?token=eyJhbGciOiJSUzI1NiJ9.e30.sig")
	if code != http.StatusUnauthorized {
		t.Fatalf("?token= must be rejected, got %d", code)
	}
	if code, _ := runWS(tickets, "/api/v1/acme/notifications/stream"); code != http.StatusUnauthorized {
		t.Fatalf("no credentials: got %d", code)
	}
}

func TestIsStreamPath(t *testing.T) {
	cases := map[string]bool{
		"/api/v1/acme/notifications/stream":                  true,
		"/api/v1/acme/riders/me/notifications/stream":        true,
		"/api/v1/acme/tracking/fleet/ws":                     true,
		"/api/v1/acme/tasks/" + uuid.NewString() + "/stream": true,
		"/api/v1/acme/tasks/":                                false,
		"/api/v1/acme/notifications":                         false,
		"/api/v1/acme/stream-ticket":                         false,
	}
	for path, want := range cases {
		if got := IsStreamPath(path); got != want {
			t.Errorf("%s: got %v want %v", path, got, want)
		}
	}
}
