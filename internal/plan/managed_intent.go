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

func DesiredComponentsForDeployment(m ManagedServicePlan) ManagedLifecyclePlan {
	return ManagedLifecyclePlan{Engine: true, Frontend: m.Frontend != nil, Ingress: true, EngineRunning: true, FrontendRunning: m.Frontend != nil, IngressServing: true}
}

func (m ManagedLifecyclePlan) Valid(action ChangeKind, service ManagedServicePlan, revision uint64) bool {
	frontend := service.Frontend != nil
	if service.State != data.EngineInitialized || m.Frontend != frontend || !m.Ingress || m.DesiredBeforeRevision != revision {
		return false
	}
	if !frontend && !m.Engine || action == RestartApp && m.Engine == frontend {
		return false
	}
	if action == StopApp {
		return !m.FrontendRunning && !m.IngressServing && (!m.Engine || !m.EngineRunning)
	}
	return (action == StartApp || action == RestartApp) && m.EngineRunning && m.FrontendRunning == frontend && m.IngressServing
}
