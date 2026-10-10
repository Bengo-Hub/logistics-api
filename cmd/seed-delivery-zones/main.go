// Command seed-delivery-zones loads a tenant's default delivery areas and quote policy.
//
// It only creates what is missing: a zone that already exists (matched by name) is left
// exactly as an admin last saved it, unless -overwrite is given. Run with -dry-run first.
//
//	go run ./cmd/seed-delivery-zones -tenant urban-loft -dry-run
//	go run ./cmd/seed-delivery-zones -tenant urban-loft
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	sharedcache "github.com/Bengo-Hub/cache"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/config"
	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/geofence"
	enttenant "github.com/bengobox/logistics-service/internal/ent/tenant"
	"github.com/bengobox/logistics-service/internal/modules/zones"
	"github.com/bengobox/logistics-service/internal/modules/zones/geo"
)

type area struct {
	Name     string
	Center   geo.Point
	RadiusM  float64
	Fee      float64
	Free     bool
	Priority int
	Status   string
	Aliases  []string
	Notes    string
}

type preset struct {
	Policy zones.Policy
	Areas  []area
}

// busiaOutlet is the Urban Loft Cafe Busia pin (auth outlet 458b9299-...).
var busiaOutlet = geo.Point{Lat: 0.4545662, Lng: 34.1272854}

// presets holds tenant defaults. Coordinates come from OpenStreetMap; areas OSM could not
// place reliably are seeded as drafts (not quoted) until an admin pins them in logistics-ui.
// Pins verified 2026-10-10 against OpenStreetMap, GeoNames and Google Maps: Bugengi and
// Redcross come from Google Maps, Malaba from GeoNames, the rest from OpenStreetMap.
var presets = map[string]preset{
	"urban-loft": {
		Policy: func() zones.Policy {
			p := zones.DefaultPolicy()
			p.Fallback, p.BufferKm, p.BaseFee, p.PerKmRate, p.MinFee, p.Rounding = zones.FallbackPerKm, 2, 0, 50, 100, 10
			return p
		}(),
		Areas: []area{
			{Name: "Busia Town", Center: busiaOutlet, RadiusM: 3000, Free: true, Priority: 10, Aliases: []string{"Busia", "Busia Township"}, Notes: "Free delivery within Busia town coverage"},
			{Name: "Bulanda", Center: geo.Point{Lat: 0.452626, Lng: 34.10268}, RadiusM: 1000, Fee: 150, Priority: 20},
			{Name: "Bugengi", Center: geo.Point{Lat: 0.44899, Lng: 34.17005}, RadiusM: 1500, Fee: 150, Priority: 20, Aliases: []string{"Bugeng'i"}, Notes: "Pinned from Google Maps (Bugeng'i sub-location, east of Busia town)."},
			{Name: "Kemodo", Center: geo.Point{Lat: 0.468094, Lng: 34.18558}, RadiusM: 1500, Fee: 200, Priority: 20, Aliases: []string{"Kemodo Market"}},
			{Name: "Ochude", Center: geo.Point{Lat: 0.565358, Lng: 34.34028}, RadiusM: 1500, Fee: 100, Priority: 20, Notes: "Ochude village, Amukura East (OpenStreetMap; the Amukura school zone lists Ochude). About 25 km out: confirm the KES 100 fee."},
			{Name: "Mauko", Center: geo.Point{Lat: 0.460717, Lng: 34.11147}, RadiusM: 1000, Fee: 100, Priority: 20, Aliases: []string{"Mauko Market"}},
			{Name: "Aget", Center: geo.Point{Lat: 0.574441, Lng: 34.16030}, RadiusM: 1500, Fee: 100, Priority: 20, Notes: "Aget village, Chakol North (OpenStreetMap). About 13 km out: confirm the KES 100 fee."},
			{Name: "Alupe", Center: geo.Point{Lat: 0.497094, Lng: 34.13416}, RadiusM: 1500, Fee: 100, Priority: 20, Aliases: []string{"Alupe Market", "Alupe University"}},
			{Name: "Adungosi", Center: geo.Point{Lat: 0.514879, Lng: 34.15113}, RadiusM: 1500, Fee: 250, Priority: 20},
			{Name: "Lukolis", Center: geo.Point{Lat: 0.553185, Lng: 34.17843}, RadiusM: 1500, Fee: 600, Priority: 20},
			{Name: "Bumala", Center: geo.Point{Lat: 0.303113, Lng: 34.20240}, RadiusM: 3000, Fee: 500, Priority: 20},
			{Name: "Matayos", Center: geo.Point{Lat: 0.364932, Lng: 34.16494}, RadiusM: 2000, Fee: 450, Priority: 20},
			{Name: "Mundika", Center: geo.Point{Lat: 0.414876, Lng: 34.14729}, RadiusM: 1500, Fee: 150, Priority: 20, Aliases: []string{"Mundika Market"}},
			{Name: "Redcross", Center: geo.Point{Lat: 0.4664098, Lng: 34.0886426}, RadiusM: 800, Fee: 150, Priority: 20, Aliases: []string{"Red Cross"}, Notes: "Pinned from Google Maps (Red Cross, Busia). Google places it on the Uganda side of the border."},
			{Name: "Malaba", Center: geo.Point{Lat: 0.63513, Lng: 34.281651}, RadiusM: 3000, Fee: 1050, Priority: 20, Notes: "Malaba town centre (GeoNames)."},
			{Name: "Funyula", Center: geo.Point{Lat: 0.280512, Lng: 34.11968}, RadiusM: 3000, Fee: 1050, Priority: 20},
			{Name: "Machakusi", Center: geo.Point{Lat: 0.609138, Lng: 34.23930}, RadiusM: 1500, Fee: 750, Priority: 20},
		},
	},
}

func main() {
	slug := flag.String("tenant", "", "tenant slug with a preset (e.g. urban-loft)")
	dryRun := flag.Bool("dry-run", false, "print what would change without writing")
	overwrite := flag.Bool("overwrite", false, "replace existing zones and policy with the preset values")
	only := flag.String("only", "", "comma-separated area names to seed; leaves the policy and other areas alone")
	flag.Parse()

	p, ok := presets[*slug]
	if !ok {
		log.Fatalf("no preset for tenant %q", *slug)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	dbURL := cfg.Postgres.URL
	if cfg.Postgres.MigrateURL != "" {
		dbURL = cfg.Postgres.MigrateURL
	}
	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	defer client.Close()

	t, err := client.Tenant.Query().Where(enttenant.Slug(*slug)).Only(ctx)
	if err != nil {
		log.Fatalf("tenant %s not found in logistics: %v", *slug, err)
	}
	logger, _ := zap.NewProduction()
	svc := zones.NewService(client, logger)
	// With Redis the writes drop the running pods' cached zones and quotes at once;
	// without it they refresh within the 5 minute TTL.
	if rdb, rerr := sharedcache.NewRedis(ctx, sharedcache.RedisConfig{
		Addr: cfg.Redis.Addr, Username: cfg.Redis.Username, Password: cfg.Redis.Password,
		DB: cfg.Redis.DB, TLS: cfg.Redis.TLSRequired, DialTimeout: cfg.Redis.DialTimeout,
	}); rerr == nil {
		svc.SetCache(sharedcache.New(rdb, logger))
	} else {
		log.Printf("redis unavailable (%v): running pods pick up changes within 5 minutes", rerr)
	}

	areas := p.Areas
	if *only != "" {
		areas = filterAreas(areas, *only)
		if len(areas) == 0 {
			log.Fatalf("-only %q matches no area in the %s preset", *only, *slug)
		}
	}
	if err := seedZones(ctx, client, svc, t.ID, areas, *dryRun, *overwrite); err != nil {
		log.Fatal(err)
	}
	if *only == "" {
		if err := seedPolicy(ctx, svc, t.ID, p.Policy, *dryRun, *overwrite); err != nil {
			log.Fatal(err)
		}
	}
	log.Printf("done (dry_run=%v)", *dryRun)
}

func seedZones(ctx context.Context, client *ent.Client, svc *zones.Service, tenantID uuid.UUID, areas []area, dryRun, overwrite bool) error {
	for _, a := range areas {
		status := a.Status
		if status == "" {
			status = zones.StatusActive
		}
		center := a.Center
		in := zones.ZoneInput{
			Name: a.Name, ZoneType: zones.TypeDelivery, Status: status, Color: colorFor(a),
			Settings: zones.ZoneSettings{
				Shape: zones.ShapeCircle, Center: &center, RadiusM: a.RadiusM, Fee: a.Fee, Free: a.Free,
				Currency: "KES", Priority: a.Priority, Aliases: a.Aliases, Notes: a.Notes,
			},
			Metadata: map[string]any{"source": "seed-delivery-zones"},
		}
		existing, err := client.GeoFence.Query().Where(geofence.TenantID(tenantID), geofence.Name(a.Name)).Only(ctx)
		switch {
		case err == nil && !overwrite:
			log.Printf("  keep    %-12s (exists, admin edits win)", a.Name)
			continue
		case err == nil && overwrite:
			log.Printf("  replace %-12s KES %-5.0f r=%.0fm %s", a.Name, a.Fee, a.RadiusM, status)
			if dryRun {
				continue
			}
			settings := in.Settings
			if _, err := svc.UpdateZone(ctx, tenantID, existing.ID, zones.ZonePatch{Status: &in.Status, Color: &in.Color, Settings: &settings, Metadata: in.Metadata}); err != nil {
				return fmt.Errorf("update %s: %w", a.Name, err)
			}
		case ent.IsNotFound(err):
			log.Printf("  create  %-12s KES %-5.0f r=%.0fm %s (%.6f, %.6f)", a.Name, a.Fee, a.RadiusM, status, a.Center.Lat, a.Center.Lng)
			if dryRun {
				continue
			}
			if _, err := svc.CreateZone(ctx, tenantID, in); err != nil {
				return fmt.Errorf("create %s: %w", a.Name, err)
			}
		default:
			return fmt.Errorf("look up %s: %w", a.Name, err)
		}
	}
	return nil
}

func seedPolicy(ctx context.Context, svc *zones.Service, tenantID uuid.UUID, p zones.Policy, dryRun, overwrite bool) error {
	current, err := svc.GetPolicy(ctx, tenantID)
	if err != nil {
		return err
	}
	if current.Source == "tenant" && !overwrite {
		log.Printf("  keep    delivery policy (tenant already has one)")
		return nil
	}
	log.Printf("  policy  per_km=%.0f min=%.0f round=%.0f buffer=%.1fkm fallback=%s", p.PerKmRate, p.MinFee, p.Rounding, p.BufferKm, p.Fallback)
	if dryRun {
		return nil
	}
	_, err = svc.SavePolicy(ctx, tenantID, p)
	return err
}

func colorFor(a area) string {
	switch {
	case a.Free:
		return "#16a34a"
	case a.Fee >= 500:
		return "#dc2626"
	case a.Fee >= 200:
		return "#f59e0b"
	default:
		return "#3b82f6"
	}
}

// filterAreas keeps the preset areas named in a comma-separated list (case-insensitive), so a
// corrected pin can be pushed without touching areas an admin has since edited.
func filterAreas(areas []area, names string) []area {
	want := map[string]bool{}
	for _, n := range strings.Split(names, ",") {
		if n = strings.ToLower(strings.TrimSpace(n)); n != "" {
			want[n] = true
		}
	}
	var out []area
	for _, a := range areas {
		if want[strings.ToLower(a.Name)] {
			out = append(out, a)
		}
	}
	return out
}
