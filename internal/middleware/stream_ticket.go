package middleware

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/redis/go-redis/v9"
)

// DefaultStreamTicketTTL is how long a stream ticket can wait before it is used.
const DefaultStreamTicketTTL = 30 * time.Second

const streamTicketKeyPrefix = "logistics:stream-ticket:"

// ErrTicketInvalid covers a ticket that is unknown, already used or expired.
var ErrTicketInvalid = errors.New("stream ticket invalid or expired")

// TicketStore keeps issued tickets. GetDel must read and delete in one atomic step so a ticket
// can only ever be used once, and must return ErrTicketInvalid when the key is absent.
type TicketStore interface {
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	GetDel(ctx context.Context, key string) ([]byte, error)
}

type redisTicketStore struct{ rdb redis.UniversalClient }

// NewRedisTicketStore stores tickets in Redis (SET with TTL, consumed with GETDEL).
func NewRedisTicketStore(rdb redis.UniversalClient) TicketStore {
	return redisTicketStore{rdb: rdb}
}

func (s redisTicketStore) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	return s.rdb.Set(ctx, key, value, ttl).Err()
}

func (s redisTicketStore) GetDel(ctx context.Context, key string) ([]byte, error) {
	b, err := s.rdb.GetDel(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, ErrTicketInvalid
	}
	return b, err
}

// StreamTickets issues and redeems the short-lived, single-use tickets that authenticate the
// WebSocket and SSE handshakes. Browsers cannot set an Authorization header on those requests,
// and an access token in the URL ends up in ingress and proxy access logs, so the client first
// trades its bearer token for an opaque ticket (POST .../stream-ticket) and connects with
// ?ticket=. The ticket is random, lives for the configured TTL (30s by default), is stored only
// as its SHA-256 hash and is deleted on first use.
type StreamTickets struct {
	store TicketStore
	ttl   time.Duration
	now   func() time.Time
}

// NewStreamTickets builds the ticket service. A ttl of zero or less uses DefaultStreamTicketTTL.
func NewStreamTickets(store TicketStore, ttl time.Duration) *StreamTickets {
	if ttl <= 0 {
		ttl = DefaultStreamTicketTTL
	}
	return &StreamTickets{store: store, ttl: ttl, now: time.Now}
}

// TTL is the lifetime of a new ticket.
func (t *StreamTickets) TTL() time.Duration { return t.ttl }

func ticketKey(ticket string) string {
	sum := sha256.Sum256([]byte(ticket))
	return streamTicketKeyPrefix + hex.EncodeToString(sum[:])
}

// Issue stores the caller's verified claims under a new ticket. The ticket never outlives the
// access token it was issued for.
func (t *StreamTickets) Issue(ctx context.Context, claims *authclient.Claims) (string, time.Duration, error) {
	if claims == nil {
		return "", 0, errors.New("no claims")
	}
	ttl := t.ttl
	if claims.RegisteredClaims.ExpiresAt != nil {
		left := claims.RegisteredClaims.ExpiresAt.Time.Sub(t.now())
		if left <= 0 {
			return "", 0, ErrTicketInvalid
		}
		if left < ttl {
			ttl = left
		}
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", 0, err
	}
	ticket := base64.RawURLEncoding.EncodeToString(raw)
	value, err := json.Marshal(claims)
	if err != nil {
		return "", 0, err
	}
	if err := t.store.Set(ctx, ticketKey(ticket), value, ttl); err != nil {
		return "", 0, err
	}
	return ticket, ttl, nil
}

// Consume redeems a ticket once and returns the claims it was issued for.
func (t *StreamTickets) Consume(ctx context.Context, ticket string) (*authclient.Claims, error) {
	if ticket == "" {
		return nil, ErrTicketInvalid
	}
	value, err := t.store.GetDel(ctx, ticketKey(ticket))
	if err != nil {
		return nil, err
	}
	var claims authclient.Claims
	if err := json.Unmarshal(value, &claims); err != nil {
		return nil, ErrTicketInvalid
	}
	if claims.RegisteredClaims.ExpiresAt != nil && !t.now().Before(claims.RegisteredClaims.ExpiresAt.Time) {
		return nil, ErrTicketInvalid
	}
	return &claims, nil
}

// IssueHandler serves POST /api/v1/{tenant}/stream-ticket behind the normal bearer auth and
// tenant middleware, answering {"ticket": "...", "expires_in": <seconds>}.
func (t *StreamTickets) IssueHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	claims, ok := authclient.ClaimsFromContext(r.Context())
	if !ok || claims.Subject == "" {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"authentication required"}`))
		return
	}
	ticket, ttl, err := t.Issue(r.Context(), claims)
	if errors.Is(err, ErrTicketInvalid) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"token expired"}`))
		return
	}
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"could not issue stream ticket"}`))
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ticket":     ticket,
		"expires_in": int(ttl.Round(time.Second) / time.Second),
	})
}
