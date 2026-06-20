package consumers

import (
	"context"
	"encoding/json"
	"time"

	eventslib "github.com/Bengo-Hub/shared-events"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/backup"
	"github.com/bengobox/logistics-service/internal/ent/backupsetting"
	"github.com/bengobox/logistics-service/internal/ent/billingevent"
	"github.com/bengobox/logistics-service/internal/ent/carrierjob"
	"github.com/bengobox/logistics-service/internal/ent/carrierpartner"
	"github.com/bengobox/logistics-service/internal/ent/chainofcustody"
	"github.com/bengobox/logistics-service/internal/ent/earningsstatement"
	"github.com/bengobox/logistics-service/internal/ent/fleet"
	"github.com/bengobox/logistics-service/internal/ent/fleetmember"
	"github.com/bengobox/logistics-service/internal/ent/geofence"
	"github.com/bengobox/logistics-service/internal/ent/integrationsetting"
	"github.com/bengobox/logistics-service/internal/ent/logisticsrole"
	"github.com/bengobox/logistics-service/internal/ent/outboxevent"
	"github.com/bengobox/logistics-service/internal/ent/outlet"
	"github.com/bengobox/logistics-service/internal/ent/pricingrule"
	"github.com/bengobox/logistics-service/internal/ent/proofofdelivery"
	"github.com/bengobox/logistics-service/internal/ent/riderrating"
	"github.com/bengobox/logistics-service/internal/ent/ridershift"
	"github.com/bengobox/logistics-service/internal/ent/rolepermission"
	"github.com/bengobox/logistics-service/internal/ent/serviceconfig"
	"github.com/bengobox/logistics-service/internal/ent/shipment"
	"github.com/bengobox/logistics-service/internal/ent/task"
	"github.com/bengobox/logistics-service/internal/ent/taskassignment"
	"github.com/bengobox/logistics-service/internal/ent/taskevent"
	"github.com/bengobox/logistics-service/internal/ent/taskstep"
	"github.com/bengobox/logistics-service/internal/ent/telemetrypoint"
	"github.com/bengobox/logistics-service/internal/ent/telemetrystream"
	"github.com/bengobox/logistics-service/internal/ent/tenant"
	"github.com/bengobox/logistics-service/internal/ent/tenantsyncevent"
	"github.com/bengobox/logistics-service/internal/ent/user"
	"github.com/bengobox/logistics-service/internal/ent/userroleassignment"
	"github.com/bengobox/logistics-service/internal/ent/vehicle"
)

const (
	tenantPurgeConsumer   = "logistics-tenant-purge"
	tenantPurgeSubject    = "tenant.purge"
	tenantPurgeAckWait    = 60 * time.Second
	tenantPurgeMaxDeliver = 5
)

// TenantPurgeConsumer subscribes to "tenant.purge" and IRREVERSIBLY deletes all of a
// tenant's logistics data when the platform owner confirms a dormancy purge.
//
// The event is emitted by subscriptions-api (aggregate_type="tenant", event_type="purge")
// only after a platform-owner /admin confirmation of an account the dormancy job already
// suspended + queued (pending_purge). Because this destroys data, the handler applies
// mandatory safety guards: a parseable tenant_id, payload.confirmed==true, and
// payload.reason=="dormancy". Anything else is acknowledged and skipped — never retried,
// never acted on.
type TenantPurgeConsumer struct {
	log    *zap.Logger
	client *ent.Client
	// svcCtx is captured during Start so the handler (no ctx parameter) can run
	// deletes under a context that respects service shutdown.
	svcCtx context.Context //nolint:containedctx
}

// NewTenantPurgeConsumer creates the consumer.
func NewTenantPurgeConsumer(log *zap.Logger, client *ent.Client) *TenantPurgeConsumer {
	return &TenantPurgeConsumer{
		log:    log.Named("consumers.tenant_purge"),
		client: client,
		svcCtx: context.Background(), // replaced by Start()
	}
}

// Start binds the durable JetStream consumer for "tenant.purge".
func (c *TenantPurgeConsumer) Start(ctx context.Context, js nats.JetStreamContext) error {
	c.svcCtx = ctx
	eventslib.SubscribeWithRebind(
		c.log,
		js,
		tenantPurgeSubject,
		c.handleMessage,
		nats.Durable(tenantPurgeConsumer),
		nats.AckExplicit(),
		nats.AckWait(tenantPurgeAckWait),
		nats.MaxDeliver(tenantPurgeMaxDeliver),
		nats.DeliverAll(),
	)
	c.log.Info("tenant purge consumer started", zap.String("durable", tenantPurgeConsumer))

	<-ctx.Done()
	return nil
}

// tenantPurgeEvent is the shared-events envelope for "tenant.purge".
// {event_type, tenant_id, payload:{tenant_id, reason, confirmed, ...}}.
type tenantPurgeEvent struct {
	EventType     string `json:"event_type"`
	AggregateType string `json:"aggregate_type"`
	TenantID      string `json:"tenant_id"`
	Payload       struct {
		TenantID  string `json:"tenant_id"`
		Reason    string `json:"reason"`
		Confirmed bool   `json:"confirmed"`
		Scope     string `json:"scope"`
	} `json:"payload"`
}

func (c *TenantPurgeConsumer) handleMessage(msg *nats.Msg) {
	ctx := c.svcCtx

	var evt tenantPurgeEvent
	if err := json.Unmarshal(msg.Data, &evt); err != nil {
		// A malformed payload will never become valid on redelivery — Ack and drop.
		c.log.Warn("tenant purge: unmarshal failed — acking", zap.Error(err))
		_ = msg.Ack()
		return
	}

	// SAFETY GUARD 1: resolve and validate the tenant id (prefer the payload copy,
	// fall back to the envelope). An empty/invalid id must NEVER delete anything —
	// an unscoped delete would wipe the whole table. Ack so it is not retried.
	rawTenantID := evt.Payload.TenantID
	if rawTenantID == "" {
		rawTenantID = evt.TenantID
	}
	tenantID, err := uuid.Parse(rawTenantID)
	if err != nil || tenantID == uuid.Nil {
		c.log.Warn("tenant purge: empty/invalid tenant_id — skipping (acked, no deletes)",
			zap.String("raw", rawTenantID))
		_ = msg.Ack()
		return
	}

	// SAFETY GUARD 2: only act on an explicitly confirmed dormancy purge. Any other
	// reason or an unconfirmed event is acknowledged and ignored — never acted on.
	if !evt.Payload.Confirmed || evt.Payload.Reason != "dormancy" {
		c.log.Warn("tenant purge: not a confirmed dormancy purge — skipping",
			zap.String("tenant_id", tenantID.String()),
			zap.Bool("confirmed", evt.Payload.Confirmed),
			zap.String("reason", evt.Payload.Reason))
		_ = msg.Ack()
		return
	}

	c.log.Warn("tenant purge: CONFIRMED dormancy purge received — deleting all tenant data (irreversible)",
		zap.String("tenant_id", tenantID.String()))

	if err := c.purgeTenant(ctx, tenantID); err != nil {
		// Transient DB error: Nak for bounded redelivery (MaxDeliver). The whole
		// purge runs in one transaction, so a failure rolls back cleanly and a retry
		// re-runs from scratch — idempotent (already-empty tables delete 0 rows).
		c.log.Error("tenant purge: delete failed — naking for retry",
			zap.String("tenant_id", tenantID.String()), zap.Error(err))
		_ = msg.Nak()
		return
	}

	c.log.Warn("tenant purge: completed — all logistics data deleted for tenant",
		zap.String("tenant_id", tenantID.String()))
	_ = msg.Ack()
}

// purgeTenant deletes every row owned by tenantID across all logistics tables in a
// single transaction, ordered to respect ON DELETE NO ACTION foreign keys (children
// before parents). Idempotent: re-running deletes 0 rows. Per-table counts are logged.
func (c *TenantPurgeConsumer) purgeTenant(ctx context.Context, tenantID uuid.UUID) error {
	tx, err := c.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit

	counts := map[string]int{}

	// Resolve the tenant's parent-row IDs up front so child tables (which carry no
	// tenant_id) can be scoped strictly by their parent's tenant-owned id.
	taskIDs, err := tx.Task.Query().Where(task.TenantID(tenantID)).IDs(ctx)
	if err != nil {
		return err
	}
	shipmentIDs, err := tx.Shipment.Query().Where(shipment.TenantID(tenantID)).IDs(ctx)
	if err != nil {
		return err
	}
	streamIDs, err := tx.TelemetryStream.Query().Where(telemetrystream.TenantID(tenantID)).IDs(ctx)
	if err != nil {
		return err
	}
	roleIDs, err := tx.LogisticsRole.Query().Where(logisticsrole.TenantID(tenantID)).IDs(ctx)
	if err != nil {
		return err
	}

	// 1. Task children (FK → tasks; ON DELETE NO ACTION). Scope by the tenant's task IDs.
	if len(taskIDs) > 0 {
		if counts["task_events"], err = tx.TaskEvent.Delete().Where(taskevent.TaskIDIn(taskIDs...)).Exec(ctx); err != nil {
			return err
		}
		if counts["task_steps"], err = tx.TaskStep.Delete().Where(taskstep.TaskIDIn(taskIDs...)).Exec(ctx); err != nil {
			return err
		}
		if counts["task_assignments"], err = tx.TaskAssignment.Delete().Where(taskassignment.TaskIDIn(taskIDs...)).Exec(ctx); err != nil {
			return err
		}
	}
	// proof_of_deliveries + rider_ratings carry tenant_id directly but also FK tasks/fleet_members.
	if counts["proof_of_deliveries"], err = tx.ProofOfDelivery.Delete().Where(proofofdelivery.TenantID(tenantID)).Exec(ctx); err != nil {
		return err
	}
	if counts["rider_ratings"], err = tx.RiderRating.Delete().Where(riderrating.TenantID(tenantID)).Exec(ctx); err != nil {
		return err
	}

	// 2. chain_of_custodies (FK → shipments). Scope by the tenant's shipment IDs.
	if len(shipmentIDs) > 0 {
		if counts["chain_of_custodies"], err = tx.ChainOfCustody.Delete().Where(chainofcustody.ShipmentIDIn(shipmentIDs...)).Exec(ctx); err != nil {
			return err
		}
	}

	// 3. telemetry_points (FK → telemetry_streams). Scope by the tenant's stream IDs.
	if len(streamIDs) > 0 {
		if counts["telemetry_points"], err = tx.TelemetryPoint.Delete().Where(telemetrypoint.StreamIDIn(streamIDs...)).Exec(ctx); err != nil {
			return err
		}
	}

	// 4. RBAC join + assignments (FK → logistics_roles / users / fleet_members).
	if counts["user_role_assignments"], err = tx.UserRoleAssignment.Delete().Where(userroleassignment.TenantID(tenantID)).Exec(ctx); err != nil {
		return err
	}
	if len(roleIDs) > 0 {
		if counts["role_permissions"], err = tx.RolePermission.Delete().Where(rolepermission.RoleIDIn(roleIDs...)).Exec(ctx); err != nil {
			return err
		}
	}

	// 5. Parent task/shipment/stream rows (children now gone).
	if counts["tasks"], err = tx.Task.Delete().Where(task.TenantID(tenantID)).Exec(ctx); err != nil {
		return err
	}
	if counts["telemetry_streams"], err = tx.TelemetryStream.Delete().Where(telemetrystream.TenantID(tenantID)).Exec(ctx); err != nil {
		return err
	}
	if counts["shipments"], err = tx.Shipment.Delete().Where(shipment.TenantID(tenantID)).Exec(ctx); err != nil {
		return err
	}

	// 6. Fleet hierarchy: fleet_members (FK → fleets/users/vehicles) → vehicles (FK → fleets) → fleets.
	if counts["fleet_members"], err = tx.FleetMember.Delete().Where(fleetmember.TenantID(tenantID)).Exec(ctx); err != nil {
		return err
	}
	if counts["vehicles"], err = tx.Vehicle.Delete().Where(vehicle.TenantID(tenantID)).Exec(ctx); err != nil {
		return err
	}
	if counts["fleets"], err = tx.Fleet.Delete().Where(fleet.TenantID(tenantID)).Exec(ctx); err != nil {
		return err
	}

	// 7. RBAC roles (referenced rows above already gone).
	if counts["logistics_roles"], err = tx.LogisticsRole.Delete().Where(logisticsrole.TenantID(tenantID)).Exec(ctx); err != nil {
		return err
	}

	// 8. Remaining flat tenant-scoped tables (no inbound FKs left).
	if counts["ridershifts"], err = tx.RiderShift.Delete().Where(ridershift.TenantID(tenantID)).Exec(ctx); err != nil {
		return err
	}
	if counts["earnings_statements"], err = tx.EarningsStatement.Delete().Where(earningsstatement.TenantID(tenantID)).Exec(ctx); err != nil {
		return err
	}
	if counts["pricing_rules"], err = tx.PricingRule.Delete().Where(pricingrule.TenantID(tenantID)).Exec(ctx); err != nil {
		return err
	}
	if counts["carrier_jobs"], err = tx.CarrierJob.Delete().Where(carrierjob.TenantID(tenantID)).Exec(ctx); err != nil {
		return err
	}
	if counts["carrier_partners"], err = tx.CarrierPartner.Delete().Where(carrierpartner.TenantID(tenantID)).Exec(ctx); err != nil {
		return err
	}
	if counts["billing_events"], err = tx.BillingEvent.Delete().Where(billingevent.TenantID(tenantID)).Exec(ctx); err != nil {
		return err
	}
	if counts["geofences"], err = tx.GeoFence.Delete().Where(geofence.TenantID(tenantID)).Exec(ctx); err != nil {
		return err
	}
	if counts["integration_settings"], err = tx.IntegrationSetting.Delete().Where(integrationsetting.TenantID(tenantID)).Exec(ctx); err != nil {
		return err
	}
	if counts["outlets"], err = tx.Outlet.Delete().Where(outlet.TenantID(tenantID)).Exec(ctx); err != nil {
		return err
	}
	if counts["service_configs"], err = tx.ServiceConfig.Delete().Where(serviceconfig.TenantID(tenantID)).Exec(ctx); err != nil {
		return err
	}
	if counts["backups"], err = tx.Backup.Delete().Where(backup.TenantID(tenantID)).Exec(ctx); err != nil {
		return err
	}
	if counts["backup_settings"], err = tx.BackupSetting.Delete().Where(backupsetting.TenantID(tenantID)).Exec(ctx); err != nil {
		return err
	}
	if counts["tenant_sync_events"], err = tx.TenantSyncEvent.Delete().Where(tenantsyncevent.TenantID(tenantID)).Exec(ctx); err != nil {
		return err
	}
	if counts["outbox_events"], err = tx.OutboxEvent.Delete().Where(outboxevent.TenantID(tenantID)).Exec(ctx); err != nil {
		return err
	}

	// 9. Users (FK → tenants). Delete after fleet_members/role assignments that reference them.
	if counts["users"], err = tx.User.Delete().Where(user.TenantID(tenantID)).Exec(ctx); err != nil {
		return err
	}

	// 10. The tenant mirror row itself (id == tenant_id). Last, after its users are gone.
	if counts["tenants"], err = tx.Tenant.Delete().Where(tenant.IDEQ(tenantID)).Exec(ctx); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	total := 0
	fields := make([]zap.Field, 0, len(counts)+2)
	fields = append(fields, zap.String("tenant_id", tenantID.String()))
	for tbl, n := range counts {
		total += n
		if n > 0 {
			fields = append(fields, zap.Int(tbl, n))
		}
	}
	fields = append(fields, zap.Int("total_rows_deleted", total))
	c.log.Warn("tenant purge: per-table delete counts", fields...)
	return nil
}
