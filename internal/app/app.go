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

	sharedcache "github.com/Bengo-Hub/cache"
	authclient "github.com/Bengo-Hub/shared-auth-client"
	eventslib "github.com/Bengo-Hub/shared-events"
	"github.com/bengobox/logistics-service/internal/config"
	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/migrate"
	handlers "github.com/bengobox/logistics-service/internal/http/handlers"
	router "github.com/bengobox/logistics-service/internal/http/router"
	"github.com/bengobox/logistics-service/internal/modules/consumers"
	"github.com/bengobox/logistics-service/internal/modules/dispatch"
	"github.com/bengobox/logistics-service/internal/modules/earnings"
	fleetmod "github.com/bengobox/logistics-service/internal/modules/fleet"
	"github.com/bengobox/logistics-service/internal/modules/identity"
	rbacmod "github.com/bengobox/logistics-service/internal/modules/rbac"
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
	cfg             *config.Config
	log             *zap.Logger
	httpServer      *http.Server
	db              *pgxpool.Pool
	entClient       *ent.Client
	cache           *redis.Client
	events          *nats.Conn
	orderConsumer    *consumers.OrderReadyConsumer
	transferConsumer *consumers.TransferReadyConsumer
	outboxPublisher *eventslib.Publisher
	etaUpdater      *dispatch.ETAUpdater
	batchScheduler  *dispatch.BatchScheduler
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

	// Ensure logistics JetStream stream exists (for fleet events)
	if natsConn != nil {
		if streamErr := events.EnsureStream(ctx, natsConn, cfg.Events); streamErr != nil {
			log.Warn("failed to ensure logistics stream", zap.Error(streamErr))
		}
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
	if cfg.Auth.EnableAPIKeyAuth {
		apiKeyValidator := authclient.NewAPIKeyValidator(cfg.Auth.ServiceURL, nil)
		authMiddleware = authclient.NewAuthMiddlewareWithAPIKey(validator, apiKeyValidator)
	} else {
		authMiddleware = authclient.NewAuthMiddleware(validator)
	}

	sqlDB, err := sql.Open("pgx", cfg.Postgres.URL)
	if err != nil {
		return nil, fmt.Errorf("sql open for ent: %w", err)
	}
	sqlDB.SetMaxOpenConns(cfg.Postgres.MaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.Postgres.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(cfg.Postgres.ConnMaxLifetime)
	sqlDB.SetConnMaxIdleTime(1 * time.Minute)
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

	tenantSyncer := tenant.NewSyncer(entClient, cfg.Auth.ServiceURL)

	// Publisher will be set after NATS initialization
	identitySvc := identity.NewService(entClient, tenantSyncer, nil, log)

	// Subscribe to auth-service events for identity sync
	if natsConn != nil {
		identityEventHandler := identity.NewEventHandler(identitySvc, log)
		if err := identityEventHandler.SubscribeToAuthEvents(natsConn); err != nil {
			log.Warn("app: failed to subscribe to auth events", zap.Error(err))
		}
	}

	// Create event publisher using shared-events outbox pattern
	var eventPublisher *events.Publisher
	var outboxPub *eventslib.Publisher
	if natsConn != nil {
		eventPublisher = events.NewPublisher(sqlDB, log)

		// Inject publisher into identity service (created before NATS init)
		identitySvc.SetPublisher(eventPublisher)

		// Start background outbox publisher
		js, jsErr := natsConn.JetStream()
		if jsErr != nil {
			log.Warn("jetstream init for outbox publisher", zap.Error(jsErr))
		} else {
			pubCfg := eventslib.DefaultPublisherConfig(js, eventPublisher.OutboxRepo(), log)
			outboxPub = eventslib.NewPublisher(pubCfg)
		}
	}

	taskSvc := tasks.NewService(entClient, log)
	taskSvc.SetPublisher(eventPublisher)

	// Earnings: records rider earnings on delivery completion, daily statement generation
	earningsSvc := earnings.NewService(entClient, log)
	taskSvc.SetEarningsService(earningsSvc)
	go earningsSvc.StartStatementJob(ctx)
	log.Info("app: earnings statement job started (daily)")

	fleetSvc := fleetmod.NewService(entClient, log, eventPublisher)
	go fleetSvc.StartStaleRiderCleanup(ctx)
	logisticsHandler := handlers.NewLogisticsHandler(log, taskSvc, fleetSvc)

	// Auto-dispatch: find nearest rider and assign tasks automatically
	autoDispatcher := dispatch.NewAutoDispatcher(log, fleetSvc, taskSvc, redisClient)

	orderConsumer := consumers.NewOrderReadyConsumer(log, taskSvc, autoDispatcher)
	transferConsumer := consumers.NewTransferReadyConsumer(log, taskSvc, autoDispatcher)

	// Initialize routing engine (Valhalla primary, no fallback initially)
	valhallaProvider := routing.NewValhallaProvider(cfg.Routing.PrimaryURL, cfg.Routing.RequestTimeout)
	routingSvc := routing.NewService(valhallaProvider, nil, redisClient, cfg.Routing.CacheTTL, log)
	routingHandler := handlers.NewRoutingHandler(routingSvc, log)

	// ETA updater: periodically recalculates ETA for in-progress deliveries
	etaUpdater := dispatch.NewETAUpdater(log, entClient, routingSvc, autoDispatcher, eventPublisher, 30*time.Second)

	// Batch scheduler: groups nearby pending tasks for the same rider every 2 min
	batchScheduler := dispatch.NewBatchScheduler(log, entClient, autoDispatcher, 2*time.Minute)

	// Public tracking handler (no auth)
	trackingHandler := handlers.NewTrackingHandler(taskSvc, log)

	// Initialize cache helper for read-heavy queries
	cacheAside := sharedcache.New(redisClient, log)

	// Zone management
	zoneSvc := zonesmod.NewService(entClient, log)
	zoneSvc.SetCache(cacheAside)
	zonesHandler := handlers.NewZonesHandler(zoneSvc, log)

	// RBAC
	rbacRepo := rbacmod.NewEntRepository(entClient)
	rbacSvc := rbacmod.NewService(rbacRepo, log, tenantSyncer)
	rbacHandler := handlers.NewRBACHandler(log, rbacSvc, rbacRepo)

	chiRouter := router.New(log, healthHandler, authMiddleware, identitySvc, logisticsHandler, routingHandler, trackingHandler, zonesHandler, rbacHandler, redisClient, cfg, cfg.HTTP.AllowedOrigins)

	httpServer := &http.Server{
		Addr:              fmt.Sprintf("%s:%d", cfg.HTTP.Host, cfg.HTTP.Port),
		Handler:           chiRouter,
		ReadTimeout:       cfg.HTTP.ReadTimeout,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      cfg.HTTP.WriteTimeout,
		IdleTimeout:       cfg.HTTP.IdleTimeout,
	}

	return &App{
		cfg:             cfg,
		log:             log,
		httpServer:      httpServer,
		db:              dbPool,
		entClient:       entClient,
		cache:           redisClient,
		events:          natsConn,
		orderConsumer:    orderConsumer,
		transferConsumer: transferConsumer,
		outboxPublisher: outboxPub,
		etaUpdater:      etaUpdater,
		batchScheduler:  batchScheduler,
	}, nil
}

func (a *App) Run(ctx context.Context) error {
	// Start outbox background publisher for logistics events
	if a.outboxPublisher != nil {
		go func() {
			if err := a.outboxPublisher.Start(ctx); err != nil {
				a.log.Error("outbox publisher stopped", zap.Error(err))
			}
		}()
		a.log.Info("outbox background publisher started")
	}

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

	// Start transfer consumer for warehouse-to-warehouse tasks from inventory
	if a.transferConsumer != nil && a.events != nil {
		js, err := a.events.JetStream()
		if err != nil {
			a.log.Warn("jetstream unavailable, transfer consumer not started", zap.Error(err))
		} else {
			go func() {
				if err := a.transferConsumer.Start(ctx, js); err != nil {
					a.log.Error("transfer consumer stopped", zap.Error(err))
				}
			}()
			a.log.Info("transfer consumer started (inventory.transfer.created)")
		}
	}

	// Start periodic ETA updater for in-progress deliveries
	if a.etaUpdater != nil {
		go a.etaUpdater.Start(ctx)
		a.log.Info("ETA updater started")
	}

	// Start batch dispatch scheduler for grouping nearby orders
	if a.batchScheduler != nil {
		go a.batchScheduler.Start(ctx)
		a.log.Info("batch scheduler started")
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
