package target

import "github.com/ShaulLavo/brine/internal/data"

// ManagedPortReservation records protected allocation intent, not a listener.
type ManagedPortReservation struct {
	App          string                `json:"app"`
	BindingID    data.ReplicaBindingID `json:"binding_id"`
	EnginePort   Port                  `json:"engine_port"`
	FrontendPort Port                  `json:"frontend_port"`
}

func (r ManagedPortReservation) Valid() bool {
	return appPattern.MatchString(r.App) && data.ValidID(string(r.BindingID)) && r.EnginePort >= 1024 && (r.FrontendPort == 0 || r.FrontendPort >= 1024 && r.FrontendPort != r.EnginePort)
}
