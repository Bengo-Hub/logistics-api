// Package settings reads the logistics service_configs table, the one place tenant settings live.
// A key resolves to the tenant's own value, else the platform default (tenant_id NULL, seeded by
// cmd/seed), else the caller's code default. Writes go through the settings handler
// (PUT/DELETE /{tenant}/settings/{key}); deleting a tenant row returns the tenant to the platform
// default.
package settings

import (
	"context"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/serviceconfig"
)

// Value returns the effective raw value of key for the tenant (uuid.Nil reads the platform
// default only). One query: the tenant row and the platform row, tenant first.
func Value(ctx context.Context, client *ent.Client, tenantID uuid.UUID, key string) (string, bool) {
	q := client.ServiceConfig.Query().Where(serviceconfig.ConfigKey(key))
	if tenantID == uuid.Nil {
		q = q.Where(serviceconfig.TenantIDIsNil())
	} else {
		q = q.Where(serviceconfig.Or(serviceconfig.TenantID(tenantID), serviceconfig.TenantIDIsNil()))
	}
	rows, err := q.Limit(2).All(ctx)
	if err != nil || len(rows) == 0 {
		return "", false
	}
	pick := rows[0]
	for _, r := range rows {
		if r.TenantID != nil {
			pick = r
		}
	}
	return strings.TrimSpace(pick.ConfigValue), true
}

// Bool reads a boolean setting; anything unset or unparsable is def.
func Bool(ctx context.Context, client *ent.Client, tenantID uuid.UUID, key string, def bool) bool {
	raw, ok := Value(ctx, client, tenantID, key)
	if !ok {
		return def
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return def
	}
	return v
}

// Int reads an integer setting within [min, max]; anything unset, unparsable or out of range is def.
func Int(ctx context.Context, client *ent.Client, tenantID uuid.UUID, key string, def, min, max int) int {
	raw, ok := Value(ctx, client, tenantID, key)
	if !ok {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < min || v > max {
		return def
	}
	return v
}

// ValidValue reports whether raw fits configType ("bool", "int"; anything else is free text), so
// a settings write can refuse a value the readers would silently ignore.
func ValidValue(configType, raw string) bool {
	raw = strings.TrimSpace(raw)
	switch configType {
	case "bool":
		_, err := strconv.ParseBool(raw)
		return err == nil
	case "int":
		_, err := strconv.Atoi(raw)
		return err == nil
	}
	return raw != ""
}
