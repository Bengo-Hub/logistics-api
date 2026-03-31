package router

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"go.uber.org/zap"

	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/Bengo-Hub/httpware"
	"github.com/redis/go-redis/v9"

	"github.com/bengobox/logistics-service/internal/http/handlers"
	appmw "github.com/bengobox/logistics-service/internal/middleware"
	"github.com/bengobox/logistics-service/internal/modules/identity"
	"github.com/bengobox/logistics-service/internal/config"
)

func New(log *zap.Logger, health *handlers.HealthHandler, authMiddleware *authclient.AuthMiddleware, idSvc *identity.Service, lh *handlers.LogisticsHandler, rh *handlers.RoutingHandler, th *handlers.TrackingHandler, zh *handlers.ZonesHandler, rbacH *handlers.RBACHandler, rdb *redis.Client, cfg *config.Config, allowedOrigins []string) http.Handler {
	rl := appmw.NewRateLimiter(rdb)
	r := chi.NewRouter()

	r.Use(chimw.RequestID)
	r.Use(chimw.RealIP)
	r.Use(httpware.RequestID)
	r.Use(httpware.Logging(log))
	r.Use(httpware.Recover(log))
	r.Use(chimw.Timeout(30 * time.Second))
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   allowedOrigins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "Origin", "X-Request-ID", "X-Tenant-ID", "X-Tenant-Slug"},
		ExposedHeaders:   []string{"Link", "X-RateLimit-Limit", "X-RateLimit-Remaining", "X-RateLimit-Reset", "Retry-After"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	r.Get("/healthz", health.Liveness)
	r.Get("/readyz", health.Readiness)
	r.Get("/metrics", health.Metrics)
	r.Get("/v1/docs/*", handlers.SwaggerUI)
	
	mediaHandler := handlers.NewMediaHandler(log, cfg)
	r.Post("/api/v1/media/upload", mediaHandler.Upload)
	
	// Serve media files
	if cfg != nil {
		fs := http.StripPrefix("/media/", http.FileServer(http.Dir(cfg.Media.Root)))
		r.Handle("/media/*", fs)
	}

	// Public tracking endpoint (no auth required)
	if th != nil {
		r.Get("/api/v1/track/{trackingCode}", th.TrackByCode)
	}

	r.Route("/api/v1", func(api chi.Router) {
		// Apply auth + subscription enforcement with granular control:
		// GET requests: auth required, subscription check skipped (read-only)
		// Mutation requests: both auth and subscription enforcement required
		if authMiddleware != nil {
			api.Use(func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					// Always require authentication
					authMiddleware.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						// Skip subscription check for GET requests (read-only)
						if r.Method == http.MethodGet {
							next.ServeHTTP(w, r)
							return
						}
						// Enforce subscription for mutation requests
						authclient.RequireActiveSubscription()(next).ServeHTTP(w, r)
					})).ServeHTTP(w, r)
				})
			})
		}

		if idSvc != nil {
			api.Use(func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					ctx := r.Context()
					claims, ok := authclient.ClaimsFromContext(ctx)
					if ok && claims.Subject != "" {
						subject, _ := uuid.Parse(claims.Subject)
						slug := claims.GetTenantSlug()
						if slug != "" {
							jitUser, err := idSvc.EnsureUserFromToken(ctx, subject, slug, map[string]any{
								"email":             claims.Email,
								"roles":             claims.Roles,
								"is_platform_owner": claims.IsPlatformOwner,
							})
							if err != nil {
								log.Warn("jit provisioning failed", zap.Error(err))
							}
							// Ensure tenant UUID is in context for downstream handlers.
							// TenantV2 middleware may only have the slug; resolve it now.
							if httpware.GetTenantID(ctx) == "" && jitUser != nil {
								tid := jitUser.TenantID
								if tid != uuid.Nil {
									ctx = httpware.WithTenantID(ctx, tid.String())
									r = r.WithContext(ctx)
								}
							}
						}
					}
					next.ServeHTTP(w, r)
				})
			})
		}

		// Serve OpenAPI spec (public, no auth required)
		api.Get("/openapi.json", handlers.OpenAPIJSON)

		api.Route("/{tenant}", func(tenant chi.Router) {
			tenant.Use(httpware.TenantV2(httpware.TenantConfig{
				ClaimsExtractor: func(ctx context.Context) (tenantID, tenantSlug string, isPlatformOwner bool, ok bool) {
					claims, found := authclient.ClaimsFromContext(ctx)
					if !found {
						return "", "", false, false
					}
					return claims.TenantID, claims.GetTenantSlug(), claims.IsPlatformOwner, true
				},
				URLParamFunc: chi.URLParam,
				Required:     true,
			}))

			// Resolve tenant slug → UUID after TenantV2 when only slug is available (fresh DB).
			if idSvc != nil {
				tenant.Use(func(next http.Handler) http.Handler {
					return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						ctx := r.Context()
						if httpware.GetTenantID(ctx) == "" {
							if slug := httpware.GetTenantSlug(ctx); slug != "" {
								if tid, err := idSvc.ResolveTenantSlug(ctx, slug); err == nil {
									ctx = httpware.WithTenantID(ctx, tid.String())
									r = r.WithContext(ctx)
								}
							}
						}
						next.ServeHTTP(w, r)
					})
				})
			}

			tenant.Route("/riders", func(riders chi.Router) {
				idHandler := handlers.NewIdentityHandler(idSvc)
				riders.Get("/me", idHandler.GetMe)
				riders.Patch("/me/profile", idHandler.UpdateProfile)
			})

			if zh != nil {
				tenant.Route("/zones", func(zoneR chi.Router) {
					zoneR.Get("/", zh.ListZones)
					zoneR.Post("/", zh.CreateZone)
					zoneR.Get("/{zoneId}", zh.GetZone)
					zoneR.Patch("/{zoneId}", zh.UpdateZone)
					zoneR.Delete("/{zoneId}", zh.DeleteZone)
				})
			}

			if rh != nil {
				tenant.Route("/routing", func(routeR chi.Router) {
					routeR.Use(appmw.RequireRateLimit(rl, "routing_requests_per_day", cfg.Subscriptions.ServiceURL+"/upgrade"))
					routeR.Get("/route", rh.GetRoute)
					routeR.Get("/eta", rh.GetETA)
					routeR.Post("/matrix", rh.GetMatrix)
					routeR.Get("/isochrone", rh.GetIsochrone)
					routeR.Get("/health", rh.HealthCheck)
				})
			}

			if th != nil {
				tenant.Route("/tracking", func(trackR chi.Router) {
					trackR.Use(appmw.RequireRateLimit(rl, "live_tracking_requests_per_day", cfg.Subscriptions.ServiceURL+"/upgrade"))
					trackR.Get("/{taskId}", th.TrackByCode)
				})
			}

			// Media upload (tenant-scoped so the rider-app path /{slug}/media/upload works)
			tenant.Post("/media/upload", mediaHandler.Upload)

			if rbacH != nil {
				rbacH.RegisterRoutes(tenant)
			}

			if lh != nil {
				tenant.Route("/tasks", func(taskR chi.Router) {
					taskR.Get("/", lh.ListTasks)
					taskR.Post("/", lh.CreateTask)
					taskR.Get("/{taskId}", lh.GetTask)
					taskR.Patch("/{taskId}/status", lh.UpdateTaskStatus)
					taskR.Post("/{taskId}/assign", lh.AssignTask)
					taskR.Post("/{taskId}/pod", lh.SubmitPoD)
					taskR.Post("/{taskId}/rate", lh.RateRider)
				})

				tenant.Route("/fleet", func(fleetR chi.Router) {
					fleetR.Get("/", lh.GetFleet)
					fleetR.Get("/members", lh.ListMembers)
					fleetR.Post("/members", lh.InviteMember)
					fleetR.Get("/members/{memberId}", lh.GetMember)
					fleetR.Post("/members/{memberId}/approve", lh.ApproveMember)
					fleetR.Post("/members/{memberId}/suspend", lh.SuspendMember)
					fleetR.Delete("/members/{memberId}", lh.DeleteMember)
				})
			}
		})
	})

	return r
}
