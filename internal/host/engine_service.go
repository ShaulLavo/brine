package host

import (
	"errors"
	"fmt"
	"github.com/ShaulLavo/brine/internal/data"
)

var errEngineSource = errors.New("engine source verification refused")

// EngineServiceBytes is one foreground owned service. It never wraps generated
// Quadlet argv; the supervisor reconstructs the fixed command from committed state.
func EngineServiceBytes(runtime data.EngineRuntime) ([]byte, error) {
	if !data.ValidID(string(runtime.BindingID)) || runtime.MemoryMB < 128 || runtime.PIDsLimit < 16 || runtime.StartupSeconds < 1 || runtime.StartupSeconds > 300 || runtime.StopSeconds < 1 || runtime.StopSeconds > 60 {
		return nil, errEngineSource
	}
	text := fmt.Sprintf("# Brine-owned engine=%s\n[Unit]\nDescription=Brine managed engine %s\n\n[Service]\nType=notify\nNotifyAccess=main\nExecStart=/usr/local/bin/brine host engine-exec %s\nRestart=on-failure\nRestartSec=5s\nKillSignal=SIGINT\nKillMode=mixed\nTimeoutStartSec=%d\nTimeoutStopSec=%d\nMemoryMax=%dM\nTasksMax=%d\nUMask=0077\nStandardOutput=null\nStandardError=null\n\n[Install]\nWantedBy=default.target\n", runtime.BindingID, runtime.BindingID, runtime.BindingID, runtime.StartupSeconds, runtime.StopSeconds, runtime.MemoryMB, runtime.PIDsLimit)
	return []byte(text), nil
}
