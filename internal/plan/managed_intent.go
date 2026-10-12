package plan

import "github.com/ShaulLavo/brine/internal/data"

// ManagedIntentFacts is protected desired-state ownership, independent of data
// provenance and observed process state. Scope mismatch carries no live flags.
type ManagedIntentFacts struct {
	Revision        uint64              `json:"revision"`
	EpochID         data.ReplicaEpochID `json:"epoch_id"`
	RuntimeSHA256   string              `json:"runtime_sha256"`
	Current         bool                `json:"current"`
	EngineRunning   bool                `json:"engine_running"`
	FrontendRunning bool                `json:"frontend_running"`
	IngressServing  bool                `json:"ingress_serving"`
}

type ManagedLifecyclePlan struct {
	Engine                bool   `json:"engine"`
	Frontend              bool   `json:"frontend"`
	Ingress               bool   `json:"ingress"`
	DesiredBeforeRevision uint64 `json:"desired_before_revision"`
	EngineRunning         bool   `json:"engine_running"`
	FrontendRunning       bool   `json:"frontend_running"`
	IngressServing        bool   `json:"ingress_serving"`
}
