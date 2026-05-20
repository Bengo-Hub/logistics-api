package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/serviceconfig"
)

// ServiceConfigHandler handles service-level configuration CRUD for logistics-api.
type ServiceConfigHandler struct {
	client *ent.Client
	logger *zap.Logger
}

// NewServiceConfigHandler creates a new ServiceConfigHandler.
func NewServiceConfigHandler(client *ent.Client, logger *zap.Logger) *ServiceConfigHandler {
	return &ServiceConfigHandler{
		client: client,
		logger: logger.Named("service-config"),
	}
}

type logisticsSCResponse struct {
	ID          uuid.UUID  `json:"id"`
	TenantID    *uuid.UUID `json:"tenant_id,omitempty"`
	ConfigKey   string     `json:"config_key"`
	ConfigValue string     `json:"config_value"`
	ConfigType  string     `json:"config_type"`
	Description string     `json:"description,omitempty"`
	IsSecret    bool       `json:"is_secret"`
	IsOverride  bool       `json:"is_override"`
	CreatedAt   string     `json:"created_at"`
	UpdatedAt   string     `json:"updated_at"`
}

func toLogisticsSCResponse(cfg *ent.ServiceConfig, isOverride bool) logisticsSCResponse {
	val := cfg.ConfigValue
	if cfg.IsSecret {
		val = "***"
	}
	return logisticsSCResponse{
		ID:          cfg.ID,
		TenantID:    cfg.TenantID,
		ConfigKey:   cfg.ConfigKey,
		ConfigValue: val,
		ConfigType:  cfg.ConfigType,
		Description: cfg.Description,
		IsSecret:    cfg.IsSecret,
		IsOverride:  isOverride,
		CreatedAt:   cfg.CreatedAt.Format("2006-01-02T15:04:05Z"),
		UpdatedAt:   cfg.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}
}

// ListPlatformSettings returns all platform-level (tenant_id=nil) service configs.
// GET /api/v1/admin/config
func (h *ServiceConfigHandler) ListPlatformSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	configs, err := h.client.ServiceConfig.Query().
		Where(serviceconfig.TenantIDIsNil()).
		All(ctx)
	if err != nil {
		h.logger.Error("failed to list platform configs", zap.Error(err))
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list platform settings"})
		return
	}

	result := make([]logisticsSCResponse, 0, len(configs))
	for _, cfg := range configs {
		result = append(result, logisticsSCResponse{
			ID:          cfg.ID,
			TenantID:    cfg.TenantID,
			ConfigKey:   cfg.ConfigKey,
			ConfigValue: cfg.ConfigValue, // unmasked for platform admin
			ConfigType:  cfg.ConfigType,
			Description: cfg.Description,
			IsSecret:    cfg.IsSecret,
			IsOverride:  false,
			CreatedAt:   cfg.CreatedAt.Format("2006-01-02T15:04:05Z"),
			UpdatedAt:   cfg.UpdatedAt.Format("2006-01-02T15:04:05Z"),
		})
	}

	respondJSON(w, http.StatusOK, map[string]any{"data": result, "total": len(result)})
}

// UpsertPlatformSetting updates a platform-level config by key.
// PUT /api/v1/admin/config/{key}
func (h *ServiceConfigHandler) UpsertPlatformSetting(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	if key == "" {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "config key is required"})
		return
	}

	var req struct {
		ConfigValue string `json:"config_value"`
		Description string `json:"description,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if req.ConfigValue == "" {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "config_value is required"})
		return
	}

	ctx := r.Context()

	existing, err := h.client.ServiceConfig.Query().
		Where(
			serviceconfig.ConfigKey(key),
			serviceconfig.TenantIDIsNil(),
		).
		First(ctx)
	if err != nil {
		respondJSON(w, http.StatusNotFound, map[string]string{"error": "platform default not found for key: " + key})
		return
	}

	update := existing.Update().SetConfigValue(req.ConfigValue)
	if req.Description != "" {
		update = update.SetDescription(req.Description)
	}

	cfg, err := update.Save(ctx)
	if err != nil {
		h.logger.Error("failed to update platform config", zap.Error(err), zap.String("key", key))
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to update platform setting"})
		return
	}

	respondJSON(w, http.StatusOK, toLogisticsSCResponse(cfg, false))
}

// ListTenantSettings returns merged tenant+platform service configs for the calling tenant.
// GET /api/v1/{tenant}/settings
func (h *ServiceConfigHandler) ListTenantSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	tenantIDStr := chi.URLParam(r, "tenant")
	tenantID, err := uuid.Parse(tenantIDStr)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid tenant ID"})
		return
	}

	platformConfigs, err := h.client.ServiceConfig.Query().
		Where(serviceconfig.TenantIDIsNil()).
		All(ctx)
	if err != nil {
		h.logger.Error("failed to list platform configs", zap.Error(err))
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list settings"})
		return
	}

	tenantConfigs, err := h.client.ServiceConfig.Query().
		Where(serviceconfig.TenantID(tenantID)).
		All(ctx)
	if err != nil {
		h.logger.Error("failed to list tenant configs", zap.Error(err))
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list settings"})
		return
	}

	tenantByKey := make(map[string]*ent.ServiceConfig, len(tenantConfigs))
	for _, tc := range tenantConfigs {
		tenantByKey[tc.ConfigKey] = tc
	}

	result := make([]logisticsSCResponse, 0, len(platformConfigs))
	for _, pc := range platformConfigs {
		if tc, ok := tenantByKey[pc.ConfigKey]; ok {
			result = append(result, toLogisticsSCResponse(tc, true))
			delete(tenantByKey, pc.ConfigKey)
		} else {
			result = append(result, toLogisticsSCResponse(pc, false))
		}
	}
	for _, tc := range tenantByKey {
		result = append(result, toLogisticsSCResponse(tc, true))
	}

	respondJSON(w, http.StatusOK, map[string]any{"data": result, "total": len(result)})
}

// UpsertTenantSetting creates or updates a tenant-specific config override.
// PUT /api/v1/{tenant}/settings/{key}
func (h *ServiceConfigHandler) UpsertTenantSetting(w http.ResponseWriter, r *http.Request) {
	tenantIDStr := chi.URLParam(r, "tenant")
	tenantID, err := uuid.Parse(tenantIDStr)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid tenant ID"})
		return
	}

	key := chi.URLParam(r, "key")
	if key == "" {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "config key is required"})
		return
	}

	var req struct {
		ConfigValue string `json:"config_value"`
		Description string `json:"description,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if req.ConfigValue == "" {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "config_value is required"})
		return
	}

	ctx := r.Context()

	platformDefault, _ := h.client.ServiceConfig.Query().
		Where(serviceconfig.ConfigKey(key), serviceconfig.TenantIDIsNil()).
		First(ctx)

	configType := "string"
	if platformDefault != nil {
		configType = platformDefault.ConfigType
	}

	existing, _ := h.client.ServiceConfig.Query().
		Where(serviceconfig.ConfigKey(key), serviceconfig.TenantID(tenantID)).
		First(ctx)

	var cfg *ent.ServiceConfig
	if existing != nil {
		cfg, err = existing.Update().SetConfigValue(req.ConfigValue).Save(ctx)
	} else {
		create := h.client.ServiceConfig.Create().
			SetTenantID(tenantID).
			SetConfigKey(key).
			SetConfigValue(req.ConfigValue).
			SetConfigType(configType)
		if req.Description != "" {
			create = create.SetDescription(req.Description)
		} else if platformDefault != nil && platformDefault.Description != "" {
			create = create.SetDescription(platformDefault.Description)
		}
		if platformDefault != nil {
			create = create.SetIsSecret(platformDefault.IsSecret)
		}
		cfg, err = create.Save(ctx)
	}

	if err != nil {
		h.logger.Error("failed to upsert tenant config", zap.Error(err), zap.String("key", key))
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to save setting"})
		return
	}

	respondJSON(w, http.StatusOK, toLogisticsSCResponse(cfg, true))
}

// RegisterPlatformRoutes registers platform admin config routes (platform owner only).
func (h *ServiceConfigHandler) RegisterPlatformRoutes(r chi.Router) {
	r.Get("/config", h.ListPlatformSettings)
	r.Put("/config/{key}", h.UpsertPlatformSetting)
}

// RegisterTenantRoutes registers tenant-scoped settings routes.
func (h *ServiceConfigHandler) RegisterTenantRoutes(r chi.Router) {
	r.Route("/settings", func(s chi.Router) {
		s.Get("/", h.ListTenantSettings)
		s.Put("/{key}", h.UpsertTenantSetting)
	})
}
