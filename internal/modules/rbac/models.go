package rbac

import (
	"time"

	"github.com/google/uuid"
)

// LogisticsRole represents a logistics service role. Roles are platform-wide: the same role
// grants the same permissions in every tenant (assignments are what is tenant-scoped).
type LogisticsRole struct {
	ID           uuid.UUID
	RoleCode     string
	Name         string
	Description  *string
	IsSystemRole bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Permission code constants follow the logistics.<module>.<action> convention
// seeded by cmd/seed/main.go.
const (
	PermTaskView     = "logistics.tasks.view"
	PermTaskAdd      = "logistics.tasks.add"
	PermTaskChange   = "logistics.tasks.change"
	PermTaskDelete   = "logistics.tasks.delete"
	PermTaskManage   = "logistics.tasks.manage"
	PermFleetView    = "logistics.fleet.view"
	PermFleetManage  = "logistics.fleet.manage"
	PermZoneView     = "logistics.zones.view"
	PermZoneManage   = "logistics.zones.manage"
	PermEarningsView = "logistics.earnings.view"
	// PermEarningsManage generates and settles statements (real payouts) and edits riders' payout details.
	PermEarningsManage = "logistics.earnings.manage"
	// PermPricingManage edits customer delivery pricing (quote policy) and rider pricing rules.
	PermPricingManage = "logistics.pricing.manage"
	PermConfigManage  = "logistics.config.manage"
)

// UserRoleAssignment represents a user role assignment.
type UserRoleAssignment struct {
	ID         uuid.UUID
	TenantID   uuid.UUID
	UserID     uuid.UUID
	RoleID     uuid.UUID
	AssignedBy uuid.UUID
	AssignedAt time.Time
	ExpiresAt  *time.Time
}
