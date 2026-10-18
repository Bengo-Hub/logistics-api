package config

import (
	"fmt"
	"time"

	"github.com/joho/godotenv"
	"github.com/kelseyhightower/envconfig"
)

const namespace = ""

// Config aggregates runtime configuration for the logistics service.
type Config struct {
	App           AppConfig
	HTTP          HTTPConfig
	Postgres      PostgresConfig
	Redis         RedisConfig
	Events        EventsConfig
	Telemetry     TelemetryConfig
	Auth          AuthConfig
	Subscriptions SubscriptionsConfig
	Routing       RoutingConfig
	Treasury      TreasuryConfig
	ERP           ERPConfig
	Notifications NotificationsConfig
}

// NotificationsConfig is the notifications-api client used for direct messages that must not
// go through the event outbox (an internal rider's account email with a temporary password).
// RiderAppURL is where that email sends the rider.
type NotificationsConfig struct {
	ServiceURL     string        `envconfig:"NOTIFICATIONS_SERVICE_URL" default:"http://notifications-api.notifications.svc.cluster.local:4000"`
	APIKey         string        `envconfig:"INTERNAL_SERVICE_KEY"`
	RequestTimeout time.Duration `envconfig:"NOTIFICATIONS_REQUEST_TIMEOUT" default:"10s"`
	RiderAppURL    string        `envconfig:"RIDER_APP_URL" default:"https://riderapp.codevertexafrica.com"`
}


// TreasuryConfig holds configuration for the treasury-api S2S client.
type TreasuryConfig struct {
	ServiceURL         string        `envconfig:"TREASURY_SERVICE_URL" default:"http://treasury-api.treasury.svc.cluster.local:4000"`
	InternalServiceKey string        `envconfig:"INTERNAL_SERVICE_KEY"`
	RequestTimeout     time.Duration `envconfig:"TREASURY_REQUEST_TIMEOUT" default:"30s"`
}

// ERPConfig holds the erp-api S2S client config. erp-api owns HR and payroll; logistics only
// raises per diem claims for staff riders there.
type ERPConfig struct {
	ServiceURL     string        `envconfig:"ERP_SERVICE_URL" default:"http://erp-api.erp.svc.cluster.local"`
	APIKey         string        `envconfig:"INTERNAL_SERVICE_KEY"`
	RequestTimeout time.Duration `envconfig:"ERP_REQUEST_TIMEOUT" default:"10s"`
}

// RoutingConfig holds configuration for the routing engine (Valhalla/OSRM).
type RoutingConfig struct {
	// Primary routing engine URL (Valhalla in-cluster)
	PrimaryURL string `envconfig:"ROUTING_PRIMARY_URL" default:"http://valhalla.logistics.svc.cluster.local:8002"`
	// Fallback routing engine URL (OSRM or Google Maps proxy)
	FallbackURL string `envconfig:"ROUTING_FALLBACK_URL"`
	// Request timeout for routing calls
	RequestTimeout time.Duration `envconfig:"ROUTING_REQUEST_TIMEOUT" default:"10s"`
	// Cache TTL for route results in Redis
	CacheTTL time.Duration `envconfig:"ROUTING_CACHE_TTL" default:"5m"`
	// Geocoder (Nominatim-compatible) used by the place search and reverse lookup proxy.
	GeocoderURL string `envconfig:"GEOCODER_URL" default:"https://nominatim.openstreetmap.org"`
	// GeocoderUserAgent identifies the platform to the geocoder, as Nominatim's usage policy requires.
	GeocoderUserAgent string `envconfig:"GEOCODER_USER_AGENT" default:"codevertex-logistics/1.0 (+https://codevertexafrica.com)"`
}

// SubscriptionsConfig holds configuration for the subscriptions enforcement client.
type SubscriptionsConfig struct {
	ServiceURL     string        `envconfig:"SUBSCRIPTION_BASE_URL" default:"https://pricingapi.codevertexafrica.com"`
	RequestTimeout time.Duration `envconfig:"SUBSCRIPTIONS_REQUEST_TIMEOUT" default:"10s"`
	APIKey         string        `envconfig:"INTERNAL_SERVICE_KEY"`
}

type AppConfig struct {
	Name    string `envconfig:"APP_NAME" default:"logistics-service"`
	Env     string `envconfig:"APP_ENV" default:"development"`
	Region  string `envconfig:"APP_REGION" default:"africa-east-1"`
	Version string `envconfig:"APP_VERSION" default:"0.1.0"`
}

type HTTPConfig struct {
	Host           string        `envconfig:"HTTP_HOST" default:"0.0.0.0"`
	Port           int           `envconfig:"HTTP_PORT" default:"4003"`
	ReadTimeout    time.Duration `envconfig:"HTTP_READ_TIMEOUT" default:"20s"`
	WriteTimeout   time.Duration `envconfig:"HTTP_WRITE_TIMEOUT" default:"20s"`
	IdleTimeout    time.Duration `envconfig:"HTTP_IDLE_TIMEOUT" default:"90s"`
	AllowedOrigins []string      `envconfig:"HTTP_ALLOWED_ORIGINS" default:"http://localhost:3000,http://localhost:3001"`
}

type PostgresConfig struct {
	URL                      string        `envconfig:"POSTGRES_URL" default:"postgres://postgres:postgres@localhost:5432/logistics?sslmode=disable"`
	MigrateURL               string        `envconfig:"POSTGRES_MIGRATE_URL"` // direct PG for migrate/seed; bypasses PgBouncer transaction mode
	MaxOpenConns             int           `envconfig:"POSTGRES_MAX_OPEN_CONNS" default:"5"`
	MaxIdleConns             int           `envconfig:"POSTGRES_MAX_IDLE_CONNS" default:"3"`
	ConnMaxLifetime          time.Duration `envconfig:"POSTGRES_CONN_MAX_LIFETIME" default:"5m"`
	StatementTimeout         time.Duration `envconfig:"POSTGRES_STATEMENT_TIMEOUT" default:"30s"`
	IdleInTransactionTimeout time.Duration `envconfig:"POSTGRES_IDLE_IN_TRANSACTION_TIMEOUT" default:"60s"`
}

type RedisConfig struct {
	Addr        string        `envconfig:"REDIS_ADDR" default:"localhost:6380"`
	Username    string        `envconfig:"REDIS_USERNAME"`
	Password    string        `envconfig:"REDIS_PASSWORD"`
	DB          int           `envconfig:"REDIS_DB" default:"0"`
	TLSRequired bool          `envconfig:"REDIS_TLS_REQUIRED" default:"false"`
	DialTimeout time.Duration `envconfig:"REDIS_DIAL_TIMEOUT" default:"5s"`
}

type EventsConfig struct {
	Bus           string `envconfig:"EVENT_BUS" default:"nats"`
	NATSURL       string `envconfig:"EVENTS_NATS_URL" default:"nats://localhost:4222"`
	StreamName    string `envconfig:"NATS_STREAM" default:"logistics"`
	DeliverGroup  string `envconfig:"NATS_DELIVER_GROUP" default:"logistics-workers"`
	DeadLetterJet string `envconfig:"NATS_DLQ_STREAM" default:"logistics-dlq"`
}

type TelemetryConfig struct {
	OTLPEndpoint string `envconfig:"OTLP_ENDPOINT"`
	MetricsURL   string `envconfig:"METRICS_ENDPOINT"`
	TracingURL   string `envconfig:"TRACING_ENDPOINT"`
}

type AuthConfig struct {
	// Auth Service SSO (JWT) integration
	ServiceURL          string        `envconfig:"AUTH_SERVICE_URL" default:"https://auth.codevertex.local:4101"`
	Issuer              string        `envconfig:"AUTH_ISSUER" default:"https://auth.codevertex.local:4101"`
	Audience            string        `envconfig:"AUTH_AUDIENCE" default:"codevertex"`
	JWKSUrl             string        `envconfig:"AUTH_JWKS_URL" default:"https://auth.codevertex.local:4101/api/v1/.well-known/jwks.json"`
	JWKSCacheTTL        time.Duration `envconfig:"AUTH_JWKS_CACHE_TTL" default:"3600s"`
	JWKSRefreshInterval time.Duration `envconfig:"AUTH_JWKS_REFRESH_INTERVAL" default:"300s"`
	// EnableAPIKeyAuth allows S2S callers to authenticate with INTERNAL_SERVICE_KEY via X-API-Key header.
	EnableAPIKeyAuth bool `envconfig:"AUTH_ENABLE_API_KEY_AUTH" default:"true"`
	// InternalServiceKey signs logistics' own S2S calls to auth-api (rider accounts).
	InternalServiceKey string `envconfig:"INTERNAL_SERVICE_KEY"`
}

// Load gathers configuration from environment variables and optional .env files.
func Load() (*Config, error) {
	_ = godotenv.Load()

	var cfg Config
	if err := envconfig.Process(namespace, &cfg); err != nil {
		return nil, fmt.Errorf("config: failed to load environment variables: %w", err)
	}

	return &cfg, nil
}
