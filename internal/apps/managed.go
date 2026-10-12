package apps

import (
	"context"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/target"
)

type ManagedStatus struct {
	BindingID    data.ReplicaBindingID           `json:"binding_id"`
	EpochID      data.ReplicaEpochID             `json:"epoch_id"`
	State        string                          `json:"state"`
	Prepared     bool                            `json:"prepared"`
	Fenced       bool                            `json:"fenced"`
	EngineUnit   string                          `json:"engine_unit"`
	EnginePort   target.Observation[target.Port] `json:"engine_port"`
	EngineActive target.Observation[bool]        `json:"engine_active"`
	Frontend     bool                            `json:"frontend"`
	FrontendPort *target.Port                    `json:"frontend_port,omitempty"`
	EngineLogs   string                          `json:"engine_logs"`
}

type ManagedReader interface {
	Read(context.Context, string) (ManagedStatus, error)
}

func (s ManagedStatus) Valid() bool {
	if !data.ValidID(string(s.BindingID)) || !data.ValidID(string(s.EpochID)) || s.EngineUnit != "brine-engine-"+string(s.BindingID)+".service" || s.EngineLogs != "unavailable" || s.State != "reserved" && s.State != "unpublished" && s.State != "initialized" {
		return false
	}
	if s.EnginePort.Status != target.KnownStatus && s.EnginePort.Status != target.Unknown || (s.EnginePort.Status == target.KnownStatus) != (s.EnginePort.Value != nil) || s.EnginePort.Value != nil && *s.EnginePort.Value < 1024 {
		return false
	}
	if s.EngineActive.Status != target.KnownStatus && s.EngineActive.Status != target.Unknown || (s.EngineActive.Status == target.KnownStatus) != (s.EngineActive.Value != nil) {
		return false
	}
	if s.State != "initialized" && s.EngineActive.Value != nil && *s.EngineActive.Value {
		return false
	}
	return s.Frontend == (s.FrontendPort != nil) && (s.FrontendPort == nil || *s.FrontendPort >= 1024 && (s.EnginePort.Value == nil || *s.FrontendPort != *s.EnginePort.Value))
}
