//go:build linux

package admission

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const DefaultDrainTimeout = 60 * time.Second

// Lease must outlive the store it protects. Close the store before this lease.
// Cancellation after acquisition never releases it on behalf of a live opener.
type Lease struct {
	file *os.File
	once sync.Once
	err  error
}

func (l *Lease) Close() error {
	l.once.Do(func() { l.err = l.file.Close() })
	return l.err
}

func (d *Directory) Acquire(ctx context.Context) (*Lease, error) {
	return d.acquire(ctx, "lock", nil)
}

func (d *Directory) AcquireExempt(ctx context.Context, e Exemption) (*Lease, error) {
	return d.acquire(ctx, "stage.lock", &e)
}

func (d *Directory) acquire(ctx context.Context, name string, e *Exemption) (_ *Lease, err error) {
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	f, err := d.openFile(name)
	if err != nil {
		return nil, err
	}
	lease := &Lease{file: f}
	defer func() {
		if err != nil {
			err = errors.Join(err, lease.Close())
		}
	}()
	if err = unix.Flock(int(f.Fd()), unix.LOCK_SH|unix.LOCK_NB); err != nil {
		return nil, lockError(err)
	}
	if err = d.checkFile(f, name); err != nil {
		return nil, err
	}
	s, err := d.Read(ctx)
	if err != nil {
		return nil, err
	}
	if e == nil && !s.Open {
		return nil, ErrClosed
	}
	if e != nil && !matchesExemption(s, *e) {
		return nil, ErrClosed
	}
	return lease, nil
}

func matchesExemption(s State, e Exemption) bool {
	p := s.Pending
	return !s.Open && p != nil && p.ExemptOpen && p.Kind == e.Kind && p.ID == e.ID && p.Stage == e.Stage && p.ExemptAttempt == e.Attempt
}

func lockError(err error) error {
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return ErrClosed
	}
	return unavailable(err)
}

func (d *Directory) drain(ctx context.Context, name string) (_ *Lease, err error) {
	f, err := d.openFile(name)
	if err != nil {
		return nil, err
	}
	lease := &Lease{file: f}
	defer func() {
		if err != nil {
			err = errors.Join(err, lease.Close())
		}
	}()
	wait, cancel := context.WithTimeout(ctx, DefaultDrainTimeout)
	defer cancel()
	if err = waitExclusive(wait, f); err != nil {
		return nil, err
	}
	if err = d.checkFile(f, name); err != nil {
		return nil, err
	}
	return lease, nil
}

func waitExclusive(ctx context.Context, f *os.File) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return errors.Join(ErrDrainTimeout, err)
		}
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return unavailable(err)
		}
		select {
		case <-ctx.Done():
			return errors.Join(ErrDrainTimeout, ctx.Err())
		case <-ticker.C:
		}
	}
}

func freshID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", unavailable(err)
	}
	return hex.EncodeToString(raw[:]), nil
}
