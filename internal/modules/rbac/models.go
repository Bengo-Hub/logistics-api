package rbac

import (
	"time"

	"github.com/google/uuid"
)

// LogisticsRole represents a logistics service role.
type LogisticsRole struct {
	ID           uuid.UUID
	TenantID     uuid.UUID
	RoleCode     string
	Name         string
	Description  *string
	IsSystemRole bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Permission code constants — follow the logistics.<module>.<action> convention
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
	PermConfigManage = "logistics.config.manage"
)

// LogisticsPermission represents a logistics service permission.
type LogisticsPermission struct {
	ID             uuid.UUID
	PermissionCode string
	Name           string
	Module         string
	Action         string
	Resource       *string
	Description    *string
	CreatedAt      time.Time
}

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
