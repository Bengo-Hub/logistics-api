package router

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"
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
	"github.com/bengobox/logistics-service/internal/modules/rbac"
	"github.com/bengobox/logistics-service/internal/config"
)

func New(log *zap.Logger, health *handlers.HealthHandler, authMiddleware *authclient.AuthMiddleware, idSvc *identity.Service, lh *handlers.LogisticsHandler, rh *handlers.RoutingHandler, th *handlers.TrackingHandler, zh *handlers.ZonesHandler, rbacH *handlers.RBACHandler, rdb *redis.Client, cfg *config.Config, allowedOrigins []string, serviceConfigH *handlers.ServiceConfigHandler, earningsH *handlers.EarningsHandler, sseH *handlers.SSEHandler, rbacSvc *rbac.Service, telH *handlers.TelemetryHandler, shipmentH *handlers.ShipmentHandler, shiftH *handlers.ShiftHandler, analyticsH *handlers.AnalyticsHandler, backupsH *handlers.BackupsHandler, backupDestH *handlers.BackupDestinationHandler) http.Handler {
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
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "Origin", "X-Request-ID", "X-Tenant-ID", "X-Tenant-Slug", "X-Outlet-ID"},
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

	// Platform admin config routes (platform owner only, outside tenant scope)
	r.Route("/api/v1/admin", func(admin chi.Router) {
		if authMiddleware != nil {
			admin.Use(authMiddleware.RequireAuth)
		}
		admin.Use(authclient.RequirePlatformOwner())
		if serviceConfigH != nil {
			serviceConfigH.RegisterPlatformRoutes(admin)
		}
		// Platform-default backup destination (OneDrive/GDrive/S3/WebDAV/SFTP/SMB).
		if backupDestH != nil {
			backupDestH.RegisterPlatformRoutes(admin)
		}
	})

	// NOTE: S2S dispatch routes (/api/v1/s2s/dispatch/...) are registered INSIDE the /api/v1 group
	// below, as a STATIC sub-route. A separate top-level r.Route("/api/v1/s2s/dispatch") is shadowed
	// by the /api/v1 mount — chi routes /api/v1/s2s/... into the /api/v1/{tenant} subrouter (matching
	// "s2s" as a tenant slug) and never reaches it, returning 404. Keeping them in-group fixes that.

	r.Route("/api/v1", func(api chi.Router) {
		// Apply auth + subscription enforcement with granular control:
		// Public GET endpoints (zones, routing, ETA) skip auth for guest checkout support.
		// Other GET requests: auth required, subscription check skipped (read-only).
		// Mutation requests: both auth and subscription enforcement required.
		if authMiddleware != nil {
			api.Use(func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					path := r.URL.Path
					// S2S dispatch endpoints authenticate via INTERNAL_SERVICE_KEY (requireServiceKey),
					// not a user JWT — skip the JWT/RBAC/subscription middleware for them.
					if strings.HasPrefix(path, "/api/v1/s2s/") {
						next.ServeHTTP(w, r)
						return
					}
					// Public read-only endpoints — skip auth entirely for guest checkout
					if r.Method == http.MethodGet && (strings.Contains(path, "/zones") ||
						strings.Contains(path, "/routing/") ||
						strings.Contains(path, "/track/")) {
						next.ServeHTTP(w, r)
						return
					}
					// All other requests require authentication
					authMiddleware.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						// GET/HEAD/OPTIONS always pass through (read-only)
						if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
							next.ServeHTTP(w, r)
							return
						}
						// Mutations: enforce an active subscription. Gating-exempt tokens
						// (platform owner, subscription-exempt, demo, service-charge) pass via
						// IsSubscriptionActive(). SEC-3 (auth-client v0.10.0): a tenant superuser
						// is NOT exempt and must hold an active subscription like any tenant user.
						claims, ok := authclient.ClaimsFromContext(r.Context())
						if !ok || claims.IsSubscriptionActive() {
							next.ServeHTTP(w, r)
							return
						}
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusForbidden)
						_, _ = w.Write([]byte(`{"error":"Your subscription is not active. Please renew to continue.","code":"subscription_inactive","upgrade":true}`))
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

		// Service-to-service dispatch (create + assign delivery tasks for external services like
		// pos-api). Registered as a STATIC /s2s/dispatch sub-route so chi matches "s2s" before the
		// "/{tenant}" param route below. Authenticated ONLY by the shared INTERNAL_SERVICE_KEY
		// (requireServiceKey); the /api/v1 JWT/RBAC/subscription middleware is skipped for /s2s/ paths.
		if lh != nil && cfg != nil && cfg.Treasury.InternalServiceKey != "" {
			api.Route("/s2s/dispatch", func(s2s chi.Router) {
				s2s.Use(requireServiceKey(cfg.Treasury.InternalServiceKey))
				s2s.Post("/{tenant}/tasks", lh.S2SCreateTask)
				s2s.Post("/{tenant}/tasks/{taskId}/assign", lh.S2SAssignTask)
			})
		}

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

			// Optional outlet context — extracts X-Outlet-ID if present
			tenant.Use(appmw.OutletContext)

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

			if idSvc != nil {
				idHandler := handlers.NewIdentityHandler(idSvc, rbacSvc)
				// Service-level auth/me: returns logistics RBAC role + permissions (Trinity Layer 3)
				tenant.Route("/auth", func(authR chi.Router) {
					authR.Get("/me", idHandler.GetAuthMe)
				})
				// Rider-specific profile (used by rider-app)
				tenant.Route("/riders", func(riders chi.Router) {
					riders.Get("/me", idHandler.GetMe)
					riders.Patch("/me/profile", idHandler.UpdateProfile)
				})
			}

			if zh != nil {
				tenant.Route("/zones", func(zoneR chi.Router) {
					zoneR.Get("/", zh.ListZones)
					zoneR.Get("/{zoneId}", zh.GetZone)
					zoneR.Group(func(mut chi.Router) {
						if rbacSvc != nil {
							mut.Use(appmw.RequirePermission(rbacSvc, rbac.PermZoneManage))
						}
						mut.Post("/", zh.CreateZone)
						mut.Patch("/{zoneId}", zh.UpdateZone)
						mut.Delete("/{zoneId}", zh.DeleteZone)
					})
				})
			}

			if rh != nil {
				tenant.Route("/routing", func(routeR chi.Router) {
					routeR.Use(appmw.RequireRateLimit(rl, "routing_requests_per_day", cfg.Subscriptions.ServiceURL+"/upgrade"))
					// Basic route/ETA stay open (used by guest checkout). The multi-stop
					// matrix optimisation is the premium "route_optimisation" surface.
					routeR.Get("/route", rh.GetRoute)
					routeR.Get("/eta", rh.GetETA)
					routeR.With(appmw.RequireFeature("route_optimisation", cfg.Subscriptions.ServiceURL+"/upgrade")).
						Post("/matrix", rh.GetMatrix)
					routeR.Get("/isochrone", rh.GetIsochrone)
					routeR.Get("/health", rh.HealthCheck)
				})
			}

			if th != nil {
				tenant.Route("/tracking", func(trackR chi.Router) {
					// Live GPS rider tracking is a premium feature. Customer-facing order
					// tracking uses the public /api/v1/track/{code} endpoint, which is unaffected.
					trackR.Use(appmw.RequireFeature("live_tracking", cfg.Subscriptions.ServiceURL+"/upgrade"))
					trackR.Use(appmw.RequireRateLimit(rl, "live_tracking_requests_per_day", cfg.Subscriptions.ServiceURL+"/upgrade"))
					trackR.Get("/{taskId}", th.TrackByCode)
				})
			}

			// Media upload (tenant-scoped so the rider-app path /{slug}/media/upload works)
			tenant.Post("/media/upload", mediaHandler.Upload)

			if rbacH != nil {
				rbacH.RegisterRoutes(tenant)
			}

			if serviceConfigH != nil {
				serviceConfigH.RegisterTenantRoutes(tenant)
			}

			// Tenant-scoped backups (this tenant's data only) — config-manage gated.
			if backupsH != nil {
				tenant.Group(func(bg chi.Router) {
					bg.Use(appmw.RequirePermission(rbacSvc, rbac.PermConfigManage))
					backupsH.RegisterRoutes(bg)
				})
			}

			// Per-tenant backup-destination override (mirrors backups off the PVC)
			// — same config-manage permission gate as the tenant backups routes.
			if backupDestH != nil {
				tenant.Group(func(bg chi.Router) {
					bg.Use(appmw.RequirePermission(rbacSvc, rbac.PermConfigManage))
					backupDestH.RegisterRoutes(bg)
				})
			}

			if earningsH != nil {
				earningsH.RegisterRoutes(tenant)
			}

			if telH != nil {
				telH.RegisterRoutes(tenant)
			}

			if shipmentH != nil {
				shipmentH.RegisterRoutes(tenant)
			}

			if shiftH != nil {
				shiftH.RegisterRoutes(tenant)
			}

			if analyticsH != nil {
				// Driver/fleet performance analytics are a premium surface.
				tenant.Group(func(ag chi.Router) {
					ag.Use(appmw.RequireAnyFeature(cfg.Subscriptions.ServiceURL+"/upgrade", "driver_analytics", "performance_reports"))
					analyticsH.RegisterRoutes(ag)
				})
			}

			if lh != nil {
				// Rider self-service: JWT-resolved tasks for the current fleet member.
				// Registered next to the other /riders/me/* routes (earnings).
				tenant.Get("/riders/me/tasks", lh.ListMyTasks)

				tenant.Route("/tasks", func(taskR chi.Router) {
					// Read-only task access
					taskR.Get("/", lh.ListTasks)
					taskR.Get("/{taskId}", lh.GetTask)
					taskR.Get("/{taskId}/pod", lh.GetPoD)
					if sseH != nil {
						taskR.Get("/{taskId}/stream", sseH.StreamTask)
					}

					// Mutations: require task management permission
					taskR.Group(func(mut chi.Router) {
						if rbacSvc != nil {
							mut.Use(appmw.RequirePermission(rbacSvc, rbac.PermTaskManage))
						}
						mut.Post("/", lh.CreateTask)
						mut.Patch("/{taskId}/status", lh.UpdateTaskStatus)
						mut.Post("/{taskId}/assign", lh.AssignTask)
						mut.Post("/{taskId}/dispatch", lh.DispatchTask)
						mut.Post("/{taskId}/pod", lh.SubmitPoD)
						mut.Post("/{taskId}/rate", lh.RateRider)
					})
				})

				tenant.Route("/fleet", func(fleetR chi.Router) {
					// Read-only fleet access
					fleetR.Get("/", lh.GetFleet)
					fleetR.Get("/members", lh.ListMembers)
					fleetR.Get("/members/{memberId}", lh.GetMember)

					// Mutations: require fleet management permission + the rider_management
					// subscription feature (cross-service tenants with only basic_logistics_access
					// can receive delivery assignments but cannot manage their own fleet).
					fleetR.Group(func(mut chi.Router) {
						if rbacSvc != nil {
							mut.Use(appmw.RequirePermission(rbacSvc, rbac.PermFleetManage))
						}
						mut.Use(appmw.RequireFeature("rider_management", cfg.Subscriptions.ServiceURL+"/upgrade"))
						mut.Post("/members", lh.InviteMember)
						mut.Post("/members/{memberId}/approve", lh.ApproveMember)
						mut.Post("/members/{memberId}/suspend", lh.SuspendMember)
						mut.Post("/members/{memberId}/reject", lh.RejectMember)
						mut.Post("/members/{memberId}/vehicle", lh.AssignVehicle)
						mut.Delete("/members/{memberId}", lh.DeleteMember)
						mut.Post("/members/batch", lh.BatchInviteMembers)
						mut.Post("/vehicles", lh.CreateVehicle)
					})
				})
			}
		})
	})

	return r
}

// requireServiceKey guards S2S routes by requiring the shared INTERNAL_SERVICE_KEY in the
// X-API-Key header, compared in constant time to avoid leaking it via timing.
func requireServiceKey(expected string) func(http.Handler) http.Handler {
	expectedBytes := []byte(expected)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			provided := r.Header.Get("X-API-Key")
			if provided == "" || subtle.ConstantTimeCompare([]byte(provided), expectedBytes) != 1 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"invalid or missing service key"}`))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}