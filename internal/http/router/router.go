package router

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"go.uber.org/zap"

	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/Bengo-Hub/httpware"
	"github.com/bengobox/logistics-service/internal/http/handlers"
	"github.com/bengobox/logistics-service/internal/modules/identity"
	"github.com/bengobox/logistics-service/internal/config"
)

func New(log *zap.Logger, health *handlers.HealthHandler, authMiddleware *authclient.AuthMiddleware, idSvc *identity.Service, lh *handlers.LogisticsHandler, cfg *config.Config) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(httpware.RequestID)
	r.Use(httpware.Logging(log))
	r.Use(httpware.Recover(log))
	r.Use(middleware.Timeout(30 * time.Second))
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Authorization", "Content-Type", "X-Tenant-ID", "X-Request-ID"},
		ExposedHeaders:   []string{"Link"},
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

	r.Route("/api/v1", func(api chi.Router) {
		// Apply auth middleware to all v1 routes
		if authMiddleware != nil {
			api.Use(authMiddleware.RequireAuth)
		}

		if idSvc != nil {
			api.Use(func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					claims, ok := authclient.ClaimsFromContext(r.Context())
					if ok && claims.Subject != "" {
						subject, _ := uuid.Parse(claims.Subject)
						slug := claims.GetTenantSlug()
						if slug != "" {
							_, err := idSvc.EnsureUserFromToken(r.Context(), subject, slug, map[string]any{
								"email": claims.Email,
							})
							if err != nil {
								log.Warn("jit provisioning failed", zap.Error(err))
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

			tenant.Route("/riders", func(riders chi.Router) {
				idHandler := handlers.NewIdentityHandler(idSvc)
				riders.Get("/me", idHandler.GetMe)
				riders.Patch("/me/profile", idHandler.UpdateProfile)
			})

			if lh != nil {
				tenant.Route("/tasks", func(taskR chi.Router) {
					taskR.Get("/", lh.ListTasks)
					taskR.Post("/", lh.CreateTask)
					taskR.Get("/{taskId}", lh.GetTask)
					taskR.Patch("/{taskId}/status", lh.UpdateTaskStatus)
					taskR.Post("/{taskId}/assign", lh.AssignTask)
					taskR.Post("/{taskId}/pod", lh.SubmitPoD)
				})

				tenant.Route("/fleet", func(fleetR chi.Router) {
					fleetR.Get("/", lh.GetFleet)
					fleetR.Get("/members", lh.ListMembers)
					fleetR.Post("/members", lh.InviteMember)
					fleetR.Get("/members/{memberId}", lh.GetMember)
					fleetR.Post("/members/{memberId}/approve", lh.ApproveMember)
					fleetR.Post("/members/{memberId}/suspend", lh.SuspendMember)
				})
			}
		})
	})

	return r
}
