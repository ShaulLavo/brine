package data

import (
	"os"
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

func (l *EngineLock) HoldsLaunch(binding ReplicaBindingID) bool {
	return l != nil && l.file != nil && l.binding == binding && l.role == ".launch" && !l.released.Load() && engineLockCurrent(l.file, l.path)
}

func (l *EngineLock) HoldsLifetime(binding ReplicaBindingID) bool {
	return l != nil && l.file != nil && l.binding == binding && l.role == ".lifetime" && !l.released.Load() && engineLockCurrent(l.file, l.path)
}
