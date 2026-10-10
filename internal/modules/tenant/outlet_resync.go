package tenant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	sharedcache "github.com/Bengo-Hub/cache"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/ent"
)

// OutletResync pulls every tenant's outlets (with their map pins) from auth-api and
// upserts them through UpsertOutlet. auth.outlet.* events keep the mirror current, but
// they age out of JetStream and pins set before an event carried them were never sent,
// so a periodic pull is what guarantees every tenant can be quoted and dispatched.
type OutletResync struct {
	client   *ent.Client
	authURL  string
	http     *http.Client
	locker   redis.UniversalClient
	onChange func(ctx context.Context, tenantID uuid.UUID)
	log      *zap.Logger
}

// NewOutletResync builds the resync. locker may be nil (every pod then runs it).
func NewOutletResync(client *ent.Client, authURL string, locker redis.UniversalClient, log *zap.Logger) *OutletResync {
	if envURL := os.Getenv("AUTH_API_URL"); envURL != "" {
		authURL = envURL
	}
	return &OutletResync{
		client:  client,
		authURL: strings.TrimRight(authURL, "/"),
		http:    &http.Client{Timeout: 15 * time.Second},
		locker:  locker,
		log:     log.Named("tenant.outlet_resync"),
	}
}

// OnChange registers the callback run for each tenant whose outlets changed.
func (r *OutletResync) OnChange(f func(ctx context.Context, tenantID uuid.UUID)) { r.onChange = f }

// authOutlet is the subset of auth-api's outlet response the mirror needs.
type authOutlet struct {
	ID        string   `json:"id"`
	Code      string   `json:"code"`
	Name      string   `json:"name"`
	UseCase   string   `json:"use_case"`
	IsHQ      bool     `json:"is_hq"`
	Status    string   `json:"status"`
	Address   *string  `json:"address"`
	Latitude  *float64 `json:"latitude"`
	Longitude *float64 `json:"longitude"`
}

// SyncTenant mirrors one tenant's outlets. Returns how many rows were written.
func (r *OutletResync) SyncTenant(ctx context.Context, tenantID uuid.UUID, slug string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.authURL+"/api/v1/tenants/"+slug+"/outlets", nil)
	if err != nil {
		return 0, err
	}
	resp, err := r.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("outlet resync %s: %w", slug, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("outlet resync %s: auth-api HTTP %d", slug, resp.StatusCode)
	}
	var outlets []authOutlet
	if err := json.NewDecoder(resp.Body).Decode(&outlets); err != nil {
		return 0, fmt.Errorf("outlet resync %s: decode: %w", slug, err)
	}
	written := 0
	for _, o := range outlets {
		id, perr := uuid.Parse(o.ID)
		if perr != nil {
			continue
		}
		rec := OutletRecord{ID: id, TenantID: tenantID, Code: o.Code, Name: o.Name, UseCase: o.UseCase, IsHQ: o.IsHQ, Status: o.Status}
		if o.Address != nil {
			rec.Address = *o.Address
		}
		if o.Latitude != nil && o.Longitude != nil && (*o.Latitude != 0 || *o.Longitude != 0) {
			rec.Latitude, rec.Longitude = o.Latitude, o.Longitude
		}
		ok, uerr := UpsertOutlet(ctx, r.client, rec)
		if uerr != nil {
			r.log.Warn("outlet upsert failed", zap.String("tenant", slug), zap.String("outlet", o.ID), zap.Error(uerr))
			continue
		}
		if ok {
			written++
		}
	}
	if written > 0 && r.onChange != nil {
		r.onChange(ctx, tenantID)
	}
	return written, nil
}

// SyncAll mirrors outlets for every tenant logistics knows about.
func (r *OutletResync) SyncAll(ctx context.Context) {
	tenants, err := r.client.Tenant.Query().All(ctx)
	if err != nil {
		r.log.Warn("outlet resync: list tenants", zap.Error(err))
		return
	}
	total := 0
	for _, t := range tenants {
		n, serr := r.SyncTenant(ctx, t.ID, t.Slug)
		if serr != nil {
			r.log.Debug("outlet resync skipped tenant", zap.String("tenant", t.Slug), zap.Error(serr))
			continue
		}
		total += n
	}
	r.log.Info("outlet resync done", zap.Int("tenants", len(tenants)), zap.Int("outlets_written", total))
}

// Start runs SyncAll shortly after startup and then every interval. A cluster-wide
// lease means one pod does the work per round.
func (r *OutletResync) Start(ctx context.Context, interval time.Duration) {
	run := func() {
		if r.locker != nil {
			lock, ok, err := sharedcache.TryLock(ctx, r.locker, "logistics:outlet-resync", 10*time.Minute)
			switch {
			case err != nil && !errors.Is(err, sharedcache.ErrLockUnavailable):
				return
			case err == nil && !ok:
				return // another pod is on it
			case err == nil:
				defer func() {
					rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
					defer cancel()
					_ = lock.Release(rctx)
				}()
			}
			// Redis unavailable: run unlocked; the upserts are idempotent.
		}
		rctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
		r.SyncAll(rctx)
	}
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
		}
		run()
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				run()
			}
		}
	}()
}
