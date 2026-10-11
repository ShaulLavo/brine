package data

import (
	"os"
	"sync"
)

// EngineLock is process-owned and closes on exec. A supervisor keeps the
// lifetime lock itself; Podman never receives authority through an inherited FD.
type EngineLock struct {
	file *os.File
	once sync.Once
	err  error
}

func (l *EngineLock) Release() error {
	l.once.Do(func() { l.err = l.file.Close() })
	return l.err
}
