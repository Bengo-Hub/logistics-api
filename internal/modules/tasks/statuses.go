package tasks

// One place for the task status vocabulary. The SLA monitor, the ETA updater, the fleet map,
// analytics and the rider rules used to keep their own copies of these lists, and they drifted:
// the SLA monitor treated "delivered" as still open, and the ETA updater only knew the legacy
// single-leg statuses.

// TerminalStatuses are statuses a task never leaves.
var TerminalStatuses = []string{"delivered", "completed", "cancelled", "failed", "returned"}

// InProgressStatuses are the statuses of a task a rider is currently working.
var InProgressStatuses = []string{
	"assigned", "accepted", "en_route", "en_route_pickup", "arrived_pickup",
	"picked_up", "en_route_dropoff", "arrived_dropoff",
}

// PrePickupStatuses are the in-progress statuses where the order is still at the outlet, so the
// job can be handed to another rider without anything to bring back.
var PrePickupStatuses = []string{"assigned", "accepted", "en_route_pickup", "arrived_pickup"}

// DeliveredStatuses count as a successful delivery in reports and earnings.
var DeliveredStatuses = []string{"delivered", "completed"}

// activeAssignmentStatuses are the assignment statuses that mean a rider holds the task.
var activeAssignmentStatuses = []string{"assigned", "accepted"}

func inList(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// IsTerminal reports whether status is final.
func IsTerminal(status string) bool { return inList(TerminalStatuses, status) }

// IsPrePickup reports whether the order is still at the outlet.
func IsPrePickup(status string) bool { return inList(PrePickupStatuses, status) }

// IsInProgress reports whether a rider is working the task.
func IsInProgress(status string) bool { return inList(InProgressStatuses, status) }

// riderStatusAllowed reports whether a rider (as opposed to a dispatcher) may move a task to
// status. Riders work their own legs and may report a failed delivery; cancelling the customer's
// delivery is a dispatcher or ordering decision, and a rider who cannot take a job declines it
// instead (DeclineTask), which hands it back for another rider.
func riderStatusAllowed(status string) bool {
	switch status {
	case "cancelled", "pending", "assigned":
		return false
	}
	return true
}

// validTransition reports whether from -> to is allowed by the task state machine.
func validTransition(from, to string) bool {
	allowed, ok := validTransitions[from]
	if !ok {
		return false
	}
	return inList(allowed, to)
}
