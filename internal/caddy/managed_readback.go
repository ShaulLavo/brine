package caddy

import (
	"context"
	"errors"
)

// LoadedConfig observes the fixed local administrative read endpoint. It has no
// mutation method or caller-selected address, body, command, or credential.
type LoadedConfig interface {
	ReadLoaded(context.Context) ([]byte, error)
}

func hasManagedSites(sites map[string]Site) bool {
	for _, site := range sites {
		if site.managed != nil {
			return true
		}
	}
	return false
}

func (m *Manager) verifyLoaded(ctx context.Context, next, previous map[string]Site) error {
	if !hasManagedSites(next) && !hasManagedSites(previous) {
		return nil
	}
	reader, ok := m.validator.(LoadedConfig)
	if !ok {
		return errors.New("caddy: managed loaded configuration unavailable")
	}
	read, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	raw, err := reader.ReadLoaded(read)
	if err != nil || read.Err() != nil {
		return errors.New("caddy: managed loaded configuration unavailable")
	}
	if err := checkAdapted(raw, next, previous); err != nil {
		return err
	}
	return admitManagedSites(ctx, next)
}

func (m *Manager) VerifyLoaded(ctx context.Context, expected State) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopped {
		return errors.New("caddy: manager requires reconciliation")
	}
	return m.verifyLoaded(ctx, expected.Sites, nil)
}

// VerifyLoadedChange proves current loaded routes against both sides, including
// domain absence when the last managed site was deliberately withdrawn.
func (m *Manager) VerifyLoadedChange(ctx context.Context, next, previous State) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopped {
		return errors.New("caddy: manager requires reconciliation")
	}
	return m.verifyLoaded(ctx, next.Sites, previous.Sites)
}

// InspectLoadedChange is read-only evidence. It validates actual disk/loaded
// semantics and logging without issuing live route/boot authority to a caller.
func (m *Manager) InspectLoadedChange(ctx context.Context, expected, previous State) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopped {
		return errors.New("caddy: manager requires reconciliation")
	}
	disk, _, err := m.observe()
	if err != nil || compareState(disk, expected) != nil {
		return errors.New("caddy: inspected generation differs")
	}
	reader, ok := m.validator.(LoadedConfig)
	if !ok {
		return errors.New("caddy: loaded inspection unavailable")
	}
	raw, err := reader.ReadLoaded(ctx)
	if err != nil {
		return err
	}
	return checkAdapted(raw, expected.Sites, previous.Sites)
}
