package target

// ControlInventory is a coherent read of durable ownership, not a runtime
// snapshot. An omitted app has no release history, UDP claims or unfinished removal.
type ControlInventory struct {
	UDPReservations     []ControlUDPReservation
	Generation          uint64
	Target              *Identity
	Apps                []ControlApp
	ManagedReservations []ManagedPortReservation
}

// Absent requires a retirement receipt when release history exists. Unknown
// includes unfinished removals. RetiredPorts let inventory detect orphan sockets
// without treating historical allocations as current reservations.
type ControlApp struct {
	Name            string
	UDP             *ControlUDPRelease
	Status          Status
	NeverReleased   bool // No release ever committed; runtime/history absence still needs proof.
	RetiredPorts    []Port
	RetiredUDP      *Observation[[]ControlUDPEndpoint]
	PortReservation *CommittedPortReservation
}

// CommittedPortReservation reserves a verified current release's port. It is
// neither a listener observation nor permission to start or retire that release.
type CommittedPortReservation struct {
	ReleaseID string `json:"release_id"`
	Port      Port   `json:"port"`
}

type ControlUDPReservation struct {
	ID          string
	App         string
	Incarnation string
	State       string
	Owner       string
	Family      string
	BindAddress string
	HostPort    Port
}

// ControlUDPRelease binds measured ownership to an immutable live head. It is
// collector input only, never a reservation or permission to publish.
type ControlUDPRelease struct {
	Incarnation string
	PlanID      string
	Units       []Unit
	Publication UDPPublication
}

type ControlUDPEndpoint struct {
	Family      string
	BindAddress string
	HostPort    Port
}
