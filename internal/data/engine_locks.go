package data

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
)

// EngineLock is process-owned and closes on exec. A supervisor keeps the
// lifetime lock itself; Podman never receives authority through an inherited FD.
type EngineLock struct {
	file     *os.File
	once     sync.Once
	err      error
	binding  ReplicaBindingID
	role     string
	path     string
	released atomic.Bool
}

func (l *EngineLock) Release() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		l.released.Store(true)
		if l.file != nil {
			l.err = l.file.Close()
		}
	})
	return l.err
}

func (l *EngineLock) HoldsLaunch(stateRoot string, binding ReplicaBindingID) bool {
	return l.holds(stateRoot, binding, ".launch")
}

func (l *EngineLock) HoldsLifetime(stateRoot string, binding ReplicaBindingID) bool {
	return l.holds(stateRoot, binding, ".lifetime")
}

func (l *EngineLock) holds(stateRoot string, binding ReplicaBindingID, role string) bool {
	return stateRoot != "" && l != nil && l.file != nil && l.binding == binding && l.role == role &&
		l.path == filepath.Join(stateRoot, "engine-locks", string(binding)+role) && !l.released.Load() && engineLockCurrent(l.file, l.path)
}
