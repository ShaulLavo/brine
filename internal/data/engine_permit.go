package data

// StartupIdentity contains shared ownership and fence proof. Runtime-specific
// proof, such as Litestream configuration, is checked by that engine afterwards.
type StartupIdentity struct {
	Engine        EngineKind
	IncarnationID AppIncarnationID
	DatabaseID    DatabaseID
	BindingID     ReplicaBindingID
	EpochID       ReplicaEpochID
	Fence         StartupFenceState
	Ownership     DestinationOwnership
	SourceSettled bool
}
type StartupFenceState string

const (
	StartupUnfenced      StartupFenceState = "unfenced"
	StartupFenceHeld     StartupFenceState = "held"
	StartupFenceReleased StartupFenceState = "released"
)

type DestinationOwnership string

const (
	DestinationLocal   DestinationOwnership = "local"
	DestinationForeign DestinationOwnership = "foreign"
)

func (p StartupIdentity) Allows(engine EngineKind) bool {
	return engine.Valid() && p.Engine == engine && ValidID(string(p.IncarnationID)) && ValidID(string(p.DatabaseID)) && ValidID(string(p.BindingID)) && ValidID(string(p.EpochID)) && (p.Fence == StartupUnfenced || p.Fence == StartupFenceReleased) && p.Ownership == DestinationLocal && p.SourceSettled
}
