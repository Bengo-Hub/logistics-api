package router

import (
	"context"
	"crypto/subtle"
	ratelimit "github.com/Bengo-Hub/shared-ratelimit"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"go.uber.org/zap"

	"github.com/Bengo-Hub/httpware"
	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/redis/go-redis/v9"

	"github.com/bengobox/logistics-service/internal/config"
	"github.com/bengobox/logistics-service/internal/http/handlers"
	appmw "github.com/bengobox/logistics-service/internal/middleware"
	"github.com/bengobox/logistics-service/internal/modules/identity"
	"github.com/bengobox/logistics-service/internal/modules/rbac"
)

func New(log *zap.Logger, health *handlers.HealthHandler, authMiddleware *authclient.AuthMiddleware, idSvc *identity.Service, lh *handlers.LogisticsHandler, rh *handlers.RoutingHandler, th *handlers.TrackingHandler, zh *handlers.ZonesHandler, rbacH *handlers.RBACHandler, rdb *redis.Client, cfg *config.Config, allowedOrigins []string, serviceConfigH *handlers.ServiceConfigHandler, earningsH *handlers.EarningsHandler, sseH *handlers.SSEHandler, rbacSvc *rbac.Service, telH *handlers.TelemetryHandler, shipmentH *handlers.ShipmentHandler, shiftH *handlers.ShiftHandler, analyticsH *handlers.AnalyticsHandler, backupsH *handlers.BackupsHandler, backupDestH *handlers.BackupDestinationHandler, validator *authclient.Validator, fleetWSH *handlers.FleetTrackingWSHandler, notifH *handlers.NotificationsHandler) http.Handler {
	// Plan limits for routing and live tracking are monthly (*_per_month in the subscriptions
	// catalog), so they count on the calendar-month window.
	quota := ratelimit.NewMonthlyQuota(rdb)
	limiter := ratelimit.NewLimiter(rdb, log, "logistics")
	r := chi.NewRouter()

	r.Use(chimw.RequestID)
	// Never chi RealIP: it trusts client-sent True-Client-IP/X-Forwarded-For.
	r.Use(ratelimit.TrustedRealIP)
	r.Use(httpware.RequestID)
	r.Use(httpware.Logging(log))
	r.Use(httpware.Recover(log))
	// Timeout cancels the request context at 30s, which killed every WebSocket and SSE stream.
	r.Use(httpware.BypassForStreaming(chimw.Timeout(30 * time.Second)))
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   allowedOrigins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "Origin", "X-Request-ID", "X-Tenant-ID", "X-Tenant-Slug", "X-Outlet-ID"},
		ExposedHeaders:   []string{"Link", "X-RateLimit-Limit", "X-RateLimit-Remaining", "X-RateLimit-Reset", "Retry-After"},
		AllowCredentials: true,
		MaxAge:           300,
	}))
	// Per-IP abuse limit (logistics had none). After CORS so 429s carry CORS headers;
	// WebSocket, SSE and health probes are exempt (ratelimit.SkipStreaming).
	r.Use(limiter.Middleware(ratelimit.IPKey, 300, time.Minute))

	r.Get("/healthz", health.Liveness)
	r.Get("/readyz", health.Readiness)
	r.Get("/metrics", health.Metrics)
	r.Get("/v1/docs/*", handlers.SwaggerUI)

	// Signed URLs for rider KYC media; every replica shares the secret (MEDIA_SIGNING_SECRET,
	// falling back to INTERNAL_SERVICE_KEY).
	var mediaSigner *httpware.MediaSigner
	if cfg != nil {
		secret := cfg.Media.SigningSecret
		if secret == "" {
			secret = cfg.Subscriptions.APIKey
		}
		mediaSigner = httpware.NewMediaSigner(secret, 12*time.Hour)
		handlers.SetMediaSigner(mediaSigner)
	}

	mediaHandler := handlers.NewMediaHandler(log, cfg)
	r.Post("/api/v1/media/upload", mediaHandler.Upload)

	// Serve media files
	if cfg != nil {
		// No directory listings; immutable caching for fingerprinted uploads.
		// Rider KYC documents are personal data: never stored by shared caches, the CDN or
		// service workers, and served only with a short-lived signed URL the API issues.
		r.Handle("/media/*", http.StripPrefix("/media", httpware.StaticMedia(cfg.Media.Root, httpware.MediaOptions{
			Private: func(p string) bool { return strings.HasPrefix(p, "/uploads/kyc/") },
			Signer:  mediaSigner,
		})))
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
		// The platform default delivery policy (validated; drops tenants' cached quotes).
		if zh != nil {
			admin.Get("/delivery-policy", zh.GetPlatformPolicy)
			admin.Put("/delivery-policy", zh.SavePlatformPolicy)
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
					// WebSocket handshakes: a browser's native WebSocket API cannot set an
					// Authorization header, so these routes authenticate via a "?token="
					// query param fallback instead (still fully verified, see AuthenticateWS's
					// own doc comment) rather than through the standard RequireAuth below. Sets
					// claims in context on success so TenantV2/RequireFeature/RequireRateLimit
					// downstream behave exactly as they do for a normal authenticated request.
					if r.Method == http.MethodGet && (strings.Contains(path, "/tracking/fleet/ws") ||
						strings.Contains(path, "/notifications/stream")) {
						if validator == nil {
							http.Error(w, `{"error":"auth not configured"}`, http.StatusInternalServerError)
							return
						}
						authedR, ok := appmw.AuthenticateWS(validator, w, r)
						if !ok {
							return
						}
						next.ServeHTTP(w, authedR)
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
						// Uniform 7-day grace: an EXPIRED tenant still within the grace
						// window passes (surfacing X-Sub-Grace-Days-Left) instead of a 403.
						if left, inGrace := claims.GraceDaysLeft(7); inGrace {
							w.Header().Set("X-Sub-Grace-Days-Left", strconv.Itoa(left))
							next.ServeHTTP(w, r)
							return
						}
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusForbidden)
						_, _ = w.Write([]byte(`{"error":"Your subscription is not active. Please renew to continue.","code":"subscription_inactive","upgrade":true}`))
					})).ServeHTTP(w, r)
				})
			})
			// Module gate: block the WHOLE logistics module (reads and writes alike) for a
			// tenant whose plan never included it. Safe at this top level: the S2S (/api/v1/s2s/)
			// and public zones/routing/track reads above skip RequireAuth entirely (no claims to
			// gate on), and logistics-ui has no service-level /auth/me — it uses SSO's own
			// /api/v1/auth/me directly for bootstrap.
			api.Use(authclient.RequireServiceAccess("logistics"))
			// Mutations-only annual support-fee gate for a perpetual/one-time-license tenant
			// (e.g. boi-enterprises on POWERSUITE_DUKA_GOLD_ONE_TIME, which already includes
			// logistics from Tier 1) whose support fee has gone unpaid past its 7-day grace
			// window — independent axis from the subscription gate above. No-ops for every
			// tenant without a support-fee obligation at all.
			api.Use(authclient.RequireSupportFeeCurrentForMutations(7))
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
		// pos-api). Registered as FLAT routes (explicit static /s2s/dispatch/... path, inline
		// requireServiceKey) rather than a nested Route/Mount — a nested Mount's "/*" catch-all
		// collides with the sibling "/{tenant}" Mount so the group middleware runs but the leaf never
		// matches (→ 404). The /api/v1 JWT/RBAC/subscription middleware is skipped for /s2s/ paths.
		if lh != nil && cfg != nil && cfg.Treasury.InternalServiceKey != "" {
			s2sKey := requireServiceKey(cfg.Treasury.InternalServiceKey)
			api.With(s2sKey).Post("/s2s/dispatch/{tenant}/tasks", lh.S2SCreateTask)
			api.With(s2sKey).Post("/s2s/dispatch/{tenant}/tasks/{taskId}/assign", lh.S2SAssignTask)
			// Live rider position and ETA for ordering's customer order tracker.
			api.With(s2sKey).Get("/s2s/dispatch/{tenant}/tasks/{taskId}/tracking", lh.S2SGetTaskTracking)
			api.With(s2sKey).Post("/s2s/dispatch/{tenant}/tasks/{taskId}/rate", lh.S2SRateRider)
		}
		// Delivery quotes and coverage for checkout in ordering, pos and any other service.
		if zh != nil && cfg != nil && cfg.Treasury.InternalServiceKey != "" {
			s2sKey := requireServiceKey(cfg.Treasury.InternalServiceKey)
			api.With(s2sKey).Post("/s2s/zones/{tenant}/quote", zh.S2SQuote)
			api.With(s2sKey).Get("/s2s/zones/{tenant}/coverage", zh.S2SCoverage)
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
				// Public, per-IP throttled: customer maps and checkout call these as guests.
				publicGeo := limiter.Middleware(ratelimit.ValueKey("geo", ratelimit.ClientIP), 120, time.Minute)
				tenant.Route("/zones", func(zoneR chi.Router) {
					zoneR.Get("/", zh.ListZones)
					zoneR.With(publicGeo).Get("/coverage", zh.Coverage)
					zoneR.With(publicGeo).Get("/quote", zh.Quote)
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
				tenant.Route("/delivery-policy", func(polR chi.Router) {
					if rbacSvc != nil {
						polR.With(appmw.RequirePermission(rbacSvc, rbac.PermZoneView)).Get("/", zh.GetPolicy)
						polR.With(appmw.RequirePermission(rbacSvc, rbac.PermPricingManage)).Put("/", zh.SavePolicy)
						polR.With(appmw.RequirePermission(rbacSvc, rbac.PermPricingManage)).Delete("/", zh.ResetPolicy)
					} else {
						polR.Get("/", zh.GetPolicy)
						polR.Put("/", zh.SavePolicy)
						polR.Delete("/", zh.ResetPolicy)
					}
				})
			}

			if rh != nil {
				tenant.Route("/routing", func(routeR chi.Router) {
					routeR.Use(appmw.RequireRateLimit(quota, "routing_requests_per_month", cfg.Subscriptions.ServiceURL+"/upgrade"))
					// Basic route/ETA stay open (used by guest checkout). The multi-stop
					// matrix optimisation is the premium "route_optimisation" surface.
					routeR.Get("/route", rh.GetRoute)
					routeR.Get("/eta", rh.GetETA)
					routeR.With(appmw.RequireFeature("route_optimisation", cfg.Subscriptions.ServiceURL+"/upgrade")).
						Post("/matrix", rh.GetMatrix)
					routeR.Get("/isochrone", rh.GetIsochrone)
					routeR.Get("/health", rh.HealthCheck)
					// Place search and reverse lookup proxy (public, per-IP throttled).
					if zh != nil {
						geocodeLimit := limiter.Middleware(ratelimit.ValueKey("geocode", ratelimit.ClientIP), 60, time.Minute)
						routeR.With(geocodeLimit).Get("/geocode/search", zh.GeocodeSearch)
						routeR.With(geocodeLimit).Get("/geocode/reverse", zh.GeocodeReverse)
					}
				})
			}

			if th != nil {
				tenant.Route("/tracking", func(trackR chi.Router) {
					// Live GPS rider tracking is a premium feature. Customer-facing order
					// tracking uses the public /api/v1/track/{code} endpoint, which is unaffected.
					trackR.Use(appmw.RequireFeature("live_tracking", cfg.Subscriptions.ServiceURL+"/upgrade"))
					trackR.Use(appmw.RequireRateLimit(quota, "live_tracking_requests_per_month", cfg.Subscriptions.ServiceURL+"/upgrade"))
					if telH != nil {
						telH.RegisterFleetTrackingRoute(trackR)
					}
					if fleetWSH != nil {
						trackR.Get("/fleet/ws", fleetWSH.ServeFleetWS)
					}

				})
			}

			if notifH != nil {
				tenant.Route("/notifications", func(notifR chi.Router) {
					// The operations feed (new jobs, failures, SLA) is for dispatchers and admins.
					if rbacSvc != nil {
						notifR.Use(appmw.RequirePermission(rbacSvc, rbac.PermTaskView))
					}
					notifR.Get("/", notifH.List)
					notifR.Get("/stream", notifH.StreamNotifications)
					notifR.Patch("/{id}/read", notifH.MarkRead)
					notifR.Post("/mark-all-read", notifH.MarkAllRead)
				})
			}

			// Media upload (tenant-scoped so the rider-app path /{slug}/media/upload works)
			tenant.Post("/media/upload", mediaHandler.Upload)

			if rbacH != nil && rbacSvc != nil {
				rbacH.RegisterRoutes(tenant, appmw.RequirePermission(rbacSvc, rbac.PermConfigManage))
			}

			if serviceConfigH != nil {
				var manageConfig func(http.Handler) http.Handler = func(h http.Handler) http.Handler { return h }
				if rbacSvc != nil {
					manageConfig = appmw.RequirePermission(rbacSvc, rbac.PermConfigManage)
				}
				serviceConfigH.RegisterTenantRoutes(tenant, manageConfig)
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
				if rbacSvc != nil {
					earningsH.SetPricingGate(appmw.RequirePermission(rbacSvc, rbac.PermPricingManage))
				}
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
				// Open jobs riders can take themselves (logistics.rider_self_claim_enabled).
				// Ownership is the caller's own fleet membership, so no task-manage permission.
				tenant.Get("/riders/me/open-tasks", lh.ListOpenJobs)
				tenant.Post("/riders/me/tasks/{taskId}/claim", lh.ClaimJob)
				tenant.Post("/riders/me/tasks/{taskId}/per-diem", lh.RaiseMyPerDiem)
				// Hand a job back before pickup; it goes to the next rider.
				tenant.Post("/riders/me/tasks/{taskId}/decline", lh.DeclineJob)
				// Cash on delivery the rider holds until it is handed in at the outlet.
				tenant.Get("/riders/me/cash", lh.GetMyCash)
				tenant.Route("/cash/riders", func(cashR chi.Router) {
					if rbacSvc != nil {
						cashR.Use(appmw.RequirePermission(rbacSvc, rbac.PermTaskManage))
					}
					// Riders hold tasks.manage too; they must not record their own hand-in.
					cashR.Use(lh.DispatcherOnly)
					cashR.Get("/", lh.ListCashWithRiders)
					cashR.Post("/{memberId}/remit", lh.RecordCashRemittance)
				})

				tenant.Route("/tasks", func(taskR chi.Router) {
					// Read-only task access. The board list is for dispatchers; riders read their
					// own jobs (riders/me/tasks) and a single task only when they hold it or could
					// claim it (checked in GetTask/GetPoD).
					taskR.With(lh.DispatcherOnly).Get("/", lh.ListTasks)
					taskR.Get("/{taskId}", lh.GetTask)
					taskR.Get("/{taskId}/pod", lh.GetPoD)
					taskR.Get("/{taskId}/tracking", lh.GetTaskTracking)
					if sseH != nil {
						taskR.Get("/{taskId}/stream", sseH.StreamTask)
					}

					// Mutations: require task management permission
					taskR.Group(func(mut chi.Router) {
						if rbacSvc != nil {
							mut.Use(appmw.RequirePermission(rbacSvc, rbac.PermTaskManage))
						}
						// Riders work their own jobs here (ownership checked in the handlers).
						mut.Patch("/{taskId}/status", lh.UpdateTaskStatus)
						mut.Post("/{taskId}/pod", lh.SubmitPoD)
						// Per diem for staff riders, raised in erp-api payroll.
						mut.Post("/{taskId}/per-diem", lh.RaisePerDiem)
						// Dispatcher actions.
						mut.Group(func(disp chi.Router) {
							disp.Use(lh.DispatcherOnly)
							disp.Post("/", lh.CreateTask)
							disp.Post("/{taskId}/assign", lh.AssignTask)
							disp.Post("/{taskId}/unassign", lh.UnassignTask)
							disp.Post("/{taskId}/cancel", lh.CancelTask)
							disp.Post("/{taskId}/dispatch", lh.DispatchTask)
							disp.Post("/{taskId}/rate", lh.RateRider)
						})
					})
				})

				tenant.Route("/fleet", func(fleetR chi.Router) {
					// Read-only fleet access
					// Rider names, phones and KYC are for the fleet's managers, not every user.
					fleetView := func(h http.HandlerFunc) http.Handler {
						if rbacSvc == nil {
							return h
						}
						return appmw.RequirePermission(rbacSvc, rbac.PermFleetView)(h)
					}
					fleetR.Method(http.MethodGet, "/", fleetView(lh.GetFleet))
					fleetR.Method(http.MethodGet, "/members", fleetView(lh.ListMembers))
					fleetR.Method(http.MethodGet, "/members/{memberId}", fleetView(lh.GetMember))

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
						mut.Put("/members/{memberId}/employment", lh.SetMemberEmployment)
						mut.Delete("/members/{memberId}", lh.DeleteMember)
						mut.Post("/members/batch", lh.BatchInviteMembers)
						mut.Post("/vehicles", lh.CreateVehicle)
						mut.Patch("/vehicles/{vehicleId}", lh.UpdateVehicle)
						mut.Delete("/vehicles/{vehicleId}", lh.DeleteVehicle)
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
