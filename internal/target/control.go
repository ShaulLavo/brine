package target

// ControlInventory is a coherent read of durable ownership, not a runtime
// snapshot. An omitted app has no release history or unfinished removal.
type ControlInventory struct {
	Generation uint64
	Apps       []ControlApp
}

// Absent requires a retirement receipt when release history exists. Unknown
// includes unfinished removals. RetiredPorts let inventory detect orphan sockets
// without treating historical allocations as current reservations.
type ControlApp struct {
	Name         string
	Status       Status
	RetiredPorts []Port
}
