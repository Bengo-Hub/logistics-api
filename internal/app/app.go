package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"entgo.io/ent/dialect/sql/schema"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/nats-io/nats.go"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/bengobox/logistics-service/internal/config"
	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/migrate"
	handlers "github.com/bengobox/logistics-service/internal/http/handlers"
	router "github.com/bengobox/logistics-service/internal/http/router"
	"github.com/bengobox/logistics-service/internal/modules/consumers"
	fleetmod "github.com/bengobox/logistics-service/internal/modules/fleet"
	"github.com/bengobox/logistics-service/internal/modules/identity"
	"github.com/bengobox/logistics-service/internal/modules/routing"
	"github.com/bengobox/logistics-service/internal/modules/tasks"
	"github.com/bengobox/logistics-service/internal/modules/tenant"
	zonesmod "github.com/bengobox/logistics-service/internal/modules/zones"
	"github.com/bengobox/logistics-service/internal/platform/cache"
	"github.com/bengobox/logistics-service/internal/platform/database"
	"github.com/bengobox/logistics-service/internal/platform/events"
	"github.com/bengobox/logistics-service/internal/platform/subscriptions"
	"github.com/bengobox/logistics-service/internal/shared/logger"
)

type App struct {
	cfg           *config.Config
	log           *zap.Logger
	httpServer    *http.Server
	db            *pgxpool.Pool
	entClient     *ent.Client
	cache         *redis.Client
	events        *nats.Conn
	orderConsumer *consumers.OrderReadyConsumer
}

func New(ctx context.Context) (*App, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}

	log, err := logger.New(cfg.App.Env)
	if err != nil {
		return nil, fmt.Errorf("logger init: %w", err)
	}

	dbPool, err := database.NewPool(ctx, cfg.Postgres)
	if err != nil {
		return nil, fmt.Errorf("postgres init: %w", err)
	}

	redisClient := cache.NewClient(cfg.Redis)

	natsConn, err := events.Connect(cfg.Events)
	if err != nil {
		log.Warn("event bus connection failed", zap.Error(err))
	}

	healthHandler := handlers.NewHealthHandler(log, dbPool, redisClient, natsConn)

	// Initialize auth-service JWT validator
	var authMiddleware *authclient.AuthMiddleware
	authConfig := authclient.DefaultConfig(
		cfg.Auth.JWKSUrl,
		cfg.Auth.Issuer,
		cfg.Auth.Audience,
	)
	authConfig.CacheTTL = cfg.Auth.JWKSCacheTTL
	authConfig.RefreshInterval = cfg.Auth.JWKSRefreshInterval

	validator, err := authclient.NewValidator(authConfig)
	if err != nil {
		return nil, fmt.Errorf("auth validator init: %w", err)
	}
	authMiddleware = authclient.NewAuthMiddleware(validator)

	sqlDB, err := sql.Open("pgx", cfg.Postgres.URL)
	if err != nil {
		return nil, fmt.Errorf("sql open for ent: %w", err)
	}
	drv := entsql.OpenDB(dialect.Postgres, sqlDB)
	entClient := ent.NewClient(ent.Driver(drv))

	// Run versioned migrations on startup (within the pod)
	if err := entClient.Schema.Create(ctx, 
		schema.WithDir(migrate.Dir),
	); err != nil {
		return nil, fmt.Errorf("ent schema create: %w", err)
	}
	log.Info("versioned migrations completed - run 'go run cmd/seed/main.go' to seed initial data (idempotent)")

	_ = subscriptions.NewClient(subscriptions.Config{
		ServiceURL:     cfg.Subscriptions.ServiceURL,
		RequestTimeout: cfg.Subscriptions.RequestTimeout,
	})

	tenantSyncer := tenant.NewSyncer(entClient)
	identitySvc := identity.NewService(entClient, tenantSyncer)

	taskSvc := tasks.NewService(entClient, log)
	fleetSvc := fleetmod.NewService(entClient, log)
	logisticsHandler := handlers.NewLogisticsHandler(log, taskSvc, fleetSvc)

	orderConsumer := consumers.NewOrderReadyConsumer(log, taskSvc)

	// Initialize routing engine (Valhalla primary, no fallback initially)
	valhallaProvider := routing.NewValhallaProvider(cfg.Routing.PrimaryURL, cfg.Routing.RequestTimeout)
	routingSvc := routing.NewService(valhallaProvider, nil, redisClient, cfg.Routing.CacheTTL, log)
	routingHandler := handlers.NewRoutingHandler(routingSvc, log)

	// Public tracking handler (no auth)
	trackingHandler := handlers.NewTrackingHandler(taskSvc, log)

	// Zone management
	zoneSvc := zonesmod.NewService(entClient, log)
	zonesHandler := handlers.NewZonesHandler(zoneSvc, log)

	chiRouter := router.New(log, healthHandler, authMiddleware, identitySvc, logisticsHandler, routingHandler, trackingHandler, zonesHandler, cfg)

	httpServer := &http.Server{
		Addr:              fmt.Sprintf("%s:%d", cfg.HTTP.Host, cfg.HTTP.Port),
		Handler:           chiRouter,
		ReadTimeout:       cfg.HTTP.ReadTimeout,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      cfg.HTTP.WriteTimeout,
		IdleTimeout:       cfg.HTTP.IdleTimeout,
	}

	return &App{
		cfg:           cfg,
		log:           log,
		httpServer:    httpServer,
		db:            dbPool,
		entClient:     entClient,
		cache:         redisClient,
		events:        natsConn,
		orderConsumer: orderConsumer,
	}, nil
}

func (a *App) Run(ctx context.Context) error {
	// Start order.ready consumer for auto-creating delivery tasks
	if a.orderConsumer != nil && a.events != nil {
		js, err := a.events.JetStream()
		if err != nil {
			a.log.Warn("jetstream unavailable, order consumer not started", zap.Error(err))
		} else {
			go func() {
				if err := a.orderConsumer.Start(ctx, js); err != nil {
					a.log.Error("order ready consumer stopped", zap.Error(err))
				}
			}()
			a.log.Info("order ready consumer started")
		}
	}

	a.log.Info("logistics service starting", zap.String("addr", a.httpServer.Addr))

	errCh := make(chan error, 1)
	go func() {
		errCh <- a.httpServer.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := a.httpServer.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("http shutdown: %w", err)
		}

		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("http server error: %w", err)
	}
}

func (a *App) Close() {
	if a.events != nil {
		if err := a.events.Drain(); err != nil {
			a.log.Warn("nats drain failed", zap.Error(err))
		}
		a.events.Close()
	}

	if a.cache != nil {
		if err := a.cache.Close(); err != nil {
			a.log.Warn("redis close failed", zap.Error(err))
		}
	}

	if a.entClient != nil {
		if err := a.entClient.Close(); err != nil {
			a.log.Warn("ent close failed", zap.Error(err))
		}
	}

	if a.db != nil {
		a.db.Close()
	}

	_ = a.log.Sync()
}
