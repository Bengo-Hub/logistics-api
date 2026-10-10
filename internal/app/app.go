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
	"github.com/google/uuid"
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
	handlers "github.com/bengobox/logistics-service/internal/http/handlers"
	router "github.com/bengobox/logistics-service/internal/http/router"
	backupmod "github.com/bengobox/logistics-service/internal/modules/backup"
	"github.com/bengobox/logistics-service/internal/modules/backup/destination"
	"github.com/bengobox/logistics-service/internal/modules/consumers"
	"github.com/bengobox/logistics-service/internal/modules/dispatch"
	"github.com/bengobox/logistics-service/internal/modules/earnings"
	fleetmod "github.com/bengobox/logistics-service/internal/modules/fleet"
	"github.com/bengobox/logistics-service/internal/modules/identity"
	notifmod "github.com/bengobox/logistics-service/internal/modules/notifications"
	rbacmod "github.com/bengobox/logistics-service/internal/modules/rbac"
	"github.com/bengobox/logistics-service/internal/modules/routing"
	"github.com/bengobox/logistics-service/internal/modules/tasks"
	telemetrymod "github.com/bengobox/logistics-service/internal/modules/telemetry"
	"github.com/bengobox/logistics-service/internal/modules/tenant"
	zonesmod "github.com/bengobox/logistics-service/internal/modules/zones"
	"github.com/bengobox/logistics-service/internal/platform/database"
	"github.com/bengobox/logistics-service/internal/platform/events"
	"github.com/bengobox/logistics-service/internal/platform/subscriptions"
	"github.com/bengobox/logistics-service/internal/shared/logger"
)

type App struct {
	cfg                 *config.Config
	log                 *zap.Logger
	httpServer          *http.Server
	db                  *pgxpool.Pool
	entClient           *ent.Client
	cache               *redis.Client
	events              *nats.Conn
	orderConsumer       *consumers.OrderReadyConsumer
	orderCancelled      *consumers.OrderCancelledConsumer
	transferConsumer    *consumers.TransferReadyConsumer
	tenantPurgeConsumer *consumers.TenantPurgeConsumer
	outboxPublisher     *eventslib.Publisher
	etaUpdater          *dispatch.ETAUpdater
	batchScheduler      *dispatch.BatchScheduler
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

	redisClient, redisErr := sharedcache.NewRedis(ctx, sharedcache.RedisConfig{
		Addr: cfg.Redis.Addr, Username: cfg.Redis.Username, Password: cfg.Redis.Password,
		DB: cfg.Redis.DB, TLS: cfg.Redis.TLSRequired, DialTimeout: cfg.Redis.DialTimeout,
	})
	if redisErr != nil {
		log.Warn("redis not reachable at startup", zap.Error(redisErr))
	}
	// Scheduled jobs run once per period fleet-wide (sharedcache.ClaimPeriod).
	sharedcache.SetLeaseClient(redisClient)

	natsConn, err := events.Connect(cfg.Events)
	if err != nil {
		log.Warn("event bus connection failed", zap.Error(err))
	}
	// One cross-replica relay for every realtime hub (fleet map, dispatcher bell, task SSE).
	var relay *eventslib.Broadcaster
	if natsConn != nil {
		relay = eventslib.NewBroadcaster(log, natsConn, "logistics")
		// Drop revoked/rotated API keys from every validator on this pod at once.
		_ = eventslib.NewBroadcaster(log, natsConn, "auth").Subscribe("apikey.changed", func(m eventslib.BroadcastMessage) {
			authclient.InvalidateAPIKeyHash(string(m.Data))
		})
	}

	// Ensure logistics JetStream stream exists (for fleet events)
	if natsConn != nil {
		if streamErr := events.EnsureStream(ctx, natsConn, cfg.Events); streamErr != nil {
			log.Warn("failed to ensure logistics stream", zap.Error(streamErr))
		}
		// Ensure the cross-service "tenant.>" stream exists so the tenant.purge
		// durable can bind and the publish is retained (subscriptions-api emits it).
		if streamErr := events.EnsureTenantStream(ctx, natsConn); streamErr != nil {
			log.Warn("failed to ensure tenant stream (tenant.purge)", zap.Error(streamErr))
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

	// Schema migrations run once per rollout in logistics-migrate (entrypoint, advisory-locked,
	// direct DSN). Running them here too, unlocked and through PgBouncer from every pod start,
	// raced the locked run on other replicas.

	subsClient := subscriptions.NewClient(subscriptions.Config{
		ServiceURL:     cfg.Subscriptions.ServiceURL,
		RequestTimeout: cfg.Subscriptions.RequestTimeout,
		APIKey:         cfg.Subscriptions.APIKey,
	})

	// consumerFeatureGate restricts cross-service data sync (ordering→delivery tasks,
	// inventory→transfer tasks) to tenants entitled to the corresponding logistics feature,
	// mirroring the HTTP-layer gating contract. Cached per tenant; fails open on a
	// subscriptions-api outage so a downtime never strands legitimate deliveries.
	consumerFeatureGate := func(ctx context.Context, tenantID, feature string) bool {
		return subsClient.ConsumerHasFeature(ctx, tenantID, feature)
	}
	// consumerActiveProductGate additionally gates on per-product ACTIVATION (has the tenant
	// currently turned the app on), distinct from plan entitlement above. Same fail-open contract.
	consumerActiveProductGate := func(ctx context.Context, tenantID, productCode string) bool {
		return subsClient.ConsumerHasActiveProduct(ctx, tenantID, productCode)
	}

	tenantSyncer := tenant.NewSyncer(entClient, cfg.Auth.ServiceURL)

	// Publisher will be set after NATS initialization
	identitySvc := identity.NewService(entClient, tenantSyncer, nil, log)

	// Cache helper for read-heavy queries, and the zones service that owns delivery
	// areas and quotes. Created before the event subscribers so outlet moves refresh it.
	cacheAside := sharedcache.New(redisClient, log)
	zoneSvc := zonesmod.NewService(entClient, log)
	zoneSvc.SetCache(cacheAside)

	// Periodic outlet pull from auth-api: events age out of JetStream, and pins set before
	// events carried them were never sent. Runs ~30s after start, then daily, one pod at a time.
	outletResync := tenant.NewOutletResync(entClient, cfg.Auth.ServiceURL, redisClient, log)
	outletResync.OnChange(zoneSvc.Invalidate)
	outletResync.Start(ctx, 24*time.Hour)

	// Subscribe to auth-service events for identity sync and outlet sync
	if natsConn != nil {
		identityEventHandler := identity.NewEventHandler(identitySvc, log)
		identityEventHandler.ProductActive = func(ctx context.Context, tenantID string) bool {
			return consumerActiveProductGate(ctx, tenantID, "logistics")
		}
		if err := identityEventHandler.SubscribeToAuthEvents(natsConn); err != nil {
			log.Warn("app: failed to subscribe to auth events", zap.Error(err))
		}

		// Outlet sync: mirrors auth.outlet.* events (with their map pin) into the local
		// outlets table so dispatch and delivery quotes can use outlet locations.
		outletSub := tenant.NewOutletSubscriber(entClient, log)
		outletSub.OnChange(zoneSvc.Invalidate)
		if err := outletSub.Start(natsConn); err != nil {
			log.Warn("app: failed to start outlet event subscriber", zap.Error(err))
		}

		// Tenant sync: mirrors auth.tenant.created/updated events into the local
		// tenants table so tenant metadata stays current without HTTP round-trips.
		tenantEventSub := tenant.NewTenantEventSubscriber(entClient, log)
		if err := tenantEventSub.Start(natsConn); err != nil {
			log.Warn("app: failed to start tenant event subscriber", zap.Error(err))
		}

		subCacheSub := subscriptions.NewCacheSubscriber(redisClient, log)
		if err := subCacheSub.Start(natsConn); err != nil {
			log.Warn("app: failed to start subscription cache subscriber", zap.Error(err))
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
	taskSvc.SetLocker(redisClient)
	taskSvc.SetAreaTagger(zoneSvc)

	// Earnings: records rider earnings on delivery completion, daily statement generation
	earningsSvc := earnings.NewService(entClient, log).WithDB(sqlDB)
	earningsSvc.SetFallbackPricer(zoneSvc.DistanceFee)
	taskSvc.SetEarningsService(earningsSvc)
	go earningsSvc.StartStatementJob(ctx)
	log.Info("app: earnings statement job started (daily)")

	fleetSvc := fleetmod.NewService(entClient, log, eventPublisher)
	go fleetSvc.StartStaleRiderCleanup(ctx)

	// Auto-dispatch: find nearest rider and assign tasks automatically
	autoDispatcher := dispatch.NewAutoDispatcher(log, fleetSvc, taskSvc, redisClient)
	autoDispatcher.SetShiftSource(entClient)

	logisticsHandler := handlers.NewLogisticsHandler(log, taskSvc, fleetSvc, autoDispatcher)
	logisticsHandler.SetTracker(autoDispatcher)
	// A job a rider declines goes straight to the next nearest rider when auto-assign is on.
	taskSvc.SetRedispatcher(func(ctx context.Context, tenantID, taskID uuid.UUID) {
		if err := autoDispatcher.DispatchTask(ctx, tenantID, taskID); err != nil {
			log.Warn("re-dispatch after decline failed", zap.String("task_id", taskID.String()), zap.Error(err))
		}
	})
	orderCancelledConsumer := consumers.NewOrderCancelledConsumer(log, taskSvc)

	orderConsumer := consumers.NewOrderReadyConsumer(log, taskSvc, autoDispatcher)
	orderConsumer.SetFeatureGate(consumerFeatureGate)
	orderConsumer.SetActiveProductGate(consumerActiveProductGate)
	transferConsumer := consumers.NewTransferReadyConsumer(log, taskSvc, autoDispatcher)
	transferConsumer.SetFeatureGate(consumerFeatureGate)

	// Tenant purge consumer: on platform-owner-confirmed dormancy purge (tenant.purge),
	// IRREVERSIBLY deletes all of the tenant's logistics data. No feature gate — a purge
	// is destructive and unconditional once its safety guards pass.
	tenantPurgeConsumer := consumers.NewTenantPurgeConsumer(log, entClient)

	// Initialize routing engine (Valhalla primary, no fallback initially)
	valhallaProvider := routing.NewValhallaProvider(cfg.Routing.PrimaryURL, cfg.Routing.RequestTimeout)
	routingSvc := routing.NewService(valhallaProvider, nil, redisClient, cfg.Routing.CacheTTL, log)
	routingHandler := handlers.NewRoutingHandler(routingSvc, log)

	// Telemetry: GPS ingestion, stream management, Redis GEO update
	telemetrySvc := telemetrymod.NewService(log, entClient, autoDispatcher)
	telemetryHandler := handlers.NewTelemetryHandler(log, telemetrySvc, entClient)
	telemetryHandler.SetDB(sqlDB)
	go telemetrySvc.StartRetentionJob(ctx, sqlDB)

	// Fleet tracking hub: real-time WebSocket push of rider location updates to
	// dispatchers on logistics-ui's live tracking map, relayed across replicas.
	fleetTrackingHub := handlers.NewFleetTrackingHub(log, relay)
	telemetryHandler.SetFleetHub(fleetTrackingHub)
	fleetTrackingWSHandler := handlers.NewFleetTrackingWSHandler(log, fleetTrackingHub, cfg.HTTP.AllowedOrigins)

	// Notifications: dispatcher-facing operational alert feed + WebSocket push, backing
	// logistics-ui's notification bell. Wired into the auto-dispatcher and SLA monitor so a
	// task that can't auto-dispatch, or breaches its SLA, actually surfaces to a dispatcher
	// instead of only a log line.
	notifHub := notifmod.NewHub(log, relay)
	notifSvc := notifmod.NewService(log, entClient, notifHub)
	autoDispatcher.SetNotifications(notifSvc)
	taskSvc.SetNotifications(notifSvc)
	notificationsHandler := handlers.NewNotificationsHandler(log, notifSvc, notifHub, cfg.HTTP.AllowedOrigins)

	// SSE hub: task status/ETA events for logistics-ui, relayed across replicas.
	sseHub := handlers.NewSSEHub(log, relay)
	sseHandler := handlers.NewSSEHandler(sseHub, log)
	taskSvc.SetSSEBroadcaster(sseHub)

	// ETA updater: periodically recalculates ETA for in-progress deliveries
	etaUpdater := dispatch.NewETAUpdater(log, entClient, routingSvc, autoDispatcher, eventPublisher, 30*time.Second)
	taskSvc.SetETATrigger(etaUpdater)

	// SLA monitor: scans for overdue tasks and publishes breach events every 5 min
	slaMonitor := tasks.NewSLAMonitor(log, entClient, eventPublisher, 5*time.Minute)
	slaMonitor.SetNotifications(notifSvc)
	go slaMonitor.Start(ctx)

	// Batch scheduler: groups nearby pending tasks for the same rider every 2 min
	batchScheduler := dispatch.NewBatchScheduler(log, entClient, autoDispatcher, 2*time.Minute)

	// Public tracking handler (no auth)
	trackingHandler := handlers.NewTrackingHandler(taskSvc, log)

	// Zone management (service created earlier so the outlet subscriber can refresh it)
	zoneSvc.SetDistanceProvider(routing.ZoneDistance{Svc: routingSvc})
	zonesHandler := handlers.NewZonesHandler(zoneSvc, log)
	zonesHandler.SetGeocoder(zonesmod.NewGeocoder(cfg.Routing.GeocoderURL, cfg.Routing.GeocoderUserAgent, zoneSvc, cacheAside, log))
	zonesHandler.SetSlugResolver(identitySvc.ResolveTenantSlug)

	// RBAC
	rbacRepo := rbacmod.NewEntRepository(entClient)
	rbacSvc := rbacmod.NewService(rbacRepo, log, tenantSyncer)
	rbacHandler := handlers.NewRBACHandler(log, rbacSvc, rbacRepo)
	logisticsHandler.SetPermissionChecker(rbacSvc)

	// Initialize service config handler for platform admin + tenant settings
	serviceConfigHandler := handlers.NewServiceConfigHandler(entClient, log)

	// Distribution / KEMSA shipments
	shipmentHandler := handlers.NewShipmentHandler(entClient, log)

	// Rider shift scheduling
	shiftHandler := handlers.NewShiftHandler(entClient, log)

	// Analytics KPI endpoint
	analyticsHandler := handlers.NewAnalyticsHandler(entClient, log)
	analyticsHandler.SetDB(sqlDB)

	earningsHandler := handlers.NewEarningsHandler(log, entClient, earningsSvc)

	// Wire treasury S2S client for rider payout disbursement
	treasuryClient := earnings.NewTreasuryClient(cfg.Treasury.ServiceURL, cfg.Treasury.InternalServiceKey)
	earningsHandler.SetTreasuryClient(treasuryClient)

	// Pluggable backup destination (OneDrive/GDrive/S3/WebDAV/SFTP/SMB) — encrypted
	// at rest with a SECRET_KEY-derived AES-256-GCM key. The handler owns the
	// destination Store; its Uploader is attached to the backup service so every
	// PVC backup is additionally mirrored best-effort.
	backupDestHandler := handlers.NewBackupDestinationHandler(entClient, destination.NewSecretKeyCipher(), log)

	// Tenant-scoped backups + daily 02:00 auto-backup scheduler + retention churn.
	backupSvc := backupmod.NewService(sqlDB, entClient, cfg.Backup.Dir, log).
		WithMirrorer(backupDestHandler.Uploader())
	backupsHandler := handlers.NewBackupsHandler(log, backupSvc, cfg.Backup.RetentionDays)
	backupmod.NewScheduler(backupSvc, backupmod.SchedulerConfig{
		Enabled:       cfg.Backup.ScheduleEnabled,
		Hour:          cfg.Backup.ScheduleHour,
		RetentionDays: cfg.Backup.RetentionDays,
	}, log).WithRedis(redisClient).Start(ctx)

	chiRouter := router.New(log, healthHandler, authMiddleware, identitySvc, logisticsHandler, routingHandler, trackingHandler, zonesHandler, rbacHandler, redisClient, cfg, cfg.HTTP.AllowedOrigins, serviceConfigHandler, earningsHandler, sseHandler, rbacSvc, telemetryHandler, shipmentHandler, shiftHandler, analyticsHandler, backupsHandler, backupDestHandler, validator, fleetTrackingWSHandler, notificationsHandler)

	httpServer := &http.Server{
		Addr:              fmt.Sprintf("%s:%d", cfg.HTTP.Host, cfg.HTTP.Port),
		Handler:           chiRouter,
		ReadTimeout:       cfg.HTTP.ReadTimeout,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      cfg.HTTP.WriteTimeout,
		IdleTimeout:       cfg.HTTP.IdleTimeout,
	}

	return &App{
		cfg:                 cfg,
		log:                 log,
		httpServer:          httpServer,
		db:                  dbPool,
		entClient:           entClient,
		cache:               redisClient,
		events:              natsConn,
		orderConsumer:       orderConsumer,
		orderCancelled:      orderCancelledConsumer,
		transferConsumer:    transferConsumer,
		tenantPurgeConsumer: tenantPurgeConsumer,
		outboxPublisher:     outboxPub,
		etaUpdater:          etaUpdater,
		batchScheduler:      batchScheduler,
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

	// Close delivery tasks whose order ordering cancelled.
	if a.orderCancelled != nil && a.events != nil {
		if js, err := a.events.JetStream(); err == nil {
			go func() {
				if err := a.orderCancelled.Start(ctx, js); err != nil {
					a.log.Error("order cancelled consumer stopped", zap.Error(err))
				}
			}()
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

	// Start tenant purge consumer — deletes all tenant data on confirmed dormancy purge
	if a.tenantPurgeConsumer != nil && a.events != nil {
		js, err := a.events.JetStream()
		if err != nil {
			a.log.Warn("jetstream unavailable, tenant purge consumer not started", zap.Error(err))
		} else {
			go func() {
				if err := a.tenantPurgeConsumer.Start(ctx, js); err != nil {
					a.log.Error("tenant purge consumer stopped", zap.Error(err))
				}
			}()
			a.log.Info("tenant purge consumer started (tenant.purge)")
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
