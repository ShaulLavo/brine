package host

import (
	"time"

	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/systemd"
)

type managedEngineObserver struct {
	state     *store.Store
	stateRoot string
	home      string
	session   localexec.Session
	units     systemd.Adapter
}

// Readiness is a bounded observation. It cannot start a native process or grant
// permission to restore a historical runtime, route, signing key, or module.
type managedCurrentProof struct {
	permit     store.EnginePermit
	observedAt time.Time
}
