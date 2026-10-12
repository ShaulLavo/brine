package host

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/ShaulLavo/brine/internal/data"
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

func (managedCurrentProof) String() string               { return "[private managed readiness observation]" }
func (managedCurrentProof) GoString() string             { return "[private managed readiness observation]" }
func (managedCurrentProof) MarshalJSON() ([]byte, error) { return nil, errEngineSource }

func managedServiceHash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func managedRuntimeHash(runtime data.EngineRuntime) (string, error) {
	raw, err := json.Marshal(runtime.Clone())
	if err != nil || runtime.Validate() != nil {
		return "", errEngineSource
	}
	return managedServiceHash(raw), nil
}
