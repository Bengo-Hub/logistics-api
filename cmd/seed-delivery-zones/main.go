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
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
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
			{Name: "Bugengi", Center: busiaOutlet, RadiusM: 1500, Fee: 150, Priority: 20, Status: zones.StatusDraft, Notes: "Location not found on OpenStreetMap. Set the centre on the map, then activate."},
			{Name: "Kemodo", Center: geo.Point{Lat: 0.468094, Lng: 34.18558}, RadiusM: 1500, Fee: 200, Priority: 20, Aliases: []string{"Kemodo Market"}},
			{Name: "Ochude", Center: busiaOutlet, RadiusM: 1500, Fee: 100, Priority: 20, Status: zones.StatusDraft, Notes: "OpenStreetMap only has an Ochude in Amukura East (0.565357, 34.34027), which does not fit a KES 100 fee. Set the centre on the map, then activate."},
			{Name: "Mauko", Center: geo.Point{Lat: 0.460717, Lng: 34.11147}, RadiusM: 1000, Fee: 100, Priority: 20, Aliases: []string{"Mauko Market"}},
			{Name: "Aget", Center: geo.Point{Lat: 0.527952, Lng: 34.20565}, RadiusM: 1500, Fee: 100, Priority: 20},
			{Name: "Alupe", Center: geo.Point{Lat: 0.497094, Lng: 34.13416}, RadiusM: 1500, Fee: 100, Priority: 20, Aliases: []string{"Alupe Market", "Alupe University"}},
			{Name: "Adungosi", Center: geo.Point{Lat: 0.514879, Lng: 34.15113}, RadiusM: 1500, Fee: 250, Priority: 20},
			{Name: "Lukolis", Center: geo.Point{Lat: 0.553185, Lng: 34.17843}, RadiusM: 1500, Fee: 600, Priority: 20},
			{Name: "Bumala", Center: geo.Point{Lat: 0.303113, Lng: 34.20240}, RadiusM: 3000, Fee: 500, Priority: 20},
			{Name: "Matayos", Center: geo.Point{Lat: 0.364932, Lng: 34.16494}, RadiusM: 2000, Fee: 450, Priority: 20},
			{Name: "Mundika", Center: geo.Point{Lat: 0.414876, Lng: 34.14729}, RadiusM: 1500, Fee: 150, Priority: 20, Aliases: []string{"Mundika Market"}},
			{Name: "Redcross", Center: busiaOutlet, RadiusM: 800, Fee: 150, Priority: 20, Status: zones.StatusDraft, Aliases: []string{"Red Cross"}, Notes: "Location not found on OpenStreetMap. Set the centre on the map, then activate."},
			{Name: "Malaba", Center: geo.Point{Lat: 0.632706, Lng: 34.26939}, RadiusM: 3000, Fee: 1050, Priority: 20},
			{Name: "Funyula", Center: geo.Point{Lat: 0.280512, Lng: 34.11968}, RadiusM: 3000, Fee: 1050, Priority: 20},
			{Name: "Machakusi", Center: geo.Point{Lat: 0.609138, Lng: 34.23930}, RadiusM: 1500, Fee: 750, Priority: 20},
		},
	},
}

func main() {
	slug := flag.String("tenant", "", "tenant slug with a preset (e.g. urban-loft)")
	dryRun := flag.Bool("dry-run", false, "print what would change without writing")
	overwrite := flag.Bool("overwrite", false, "replace existing zones and policy with the preset values")
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
	svc := zones.NewService(client, logger) // no cache: running pods refresh within the 5 minute TTL

	if err := seedZones(ctx, client, svc, t.ID, p.Areas, *dryRun, *overwrite); err != nil {
		log.Fatal(err)
	}
	if err := seedPolicy(ctx, svc, t.ID, p.Policy, *dryRun, *overwrite); err != nil {
		log.Fatal(err)
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
