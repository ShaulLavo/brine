package enroll

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ShaulLavo/brine/internal/localexec"
)

type serviceState string

const (
	serviceActive        serviceState = "active"
	serviceEnabled       serviceState = "enabled"
	serviceMasked        serviceState = "masked"
	serviceMaskedRuntime serviceState = "masked-runtime"
)

func (s serviceState) masked() bool { return s == serviceMasked || s == serviceMaskedRuntime }

type caddyServiceState struct{ active, enabled serviceState }

func (h *host) originalCaddyState(ctx context.Context) (caddyServiceState, error) {
	active, err := h.observeService(ctx, "is-active")
	if err != nil {
		return caddyServiceState{}, err
	}
	enabled, err := h.observeService(ctx, "is-enabled")
	if err != nil {
		return caddyServiceState{}, err
	}
	if enabled.masked() {
		return caddyServiceState{}, errors.New("preexisting Caddy mask refused")
	}
	return caddyServiceState{active: active, enabled: enabled}, nil
}

func (h *host) observeService(ctx context.Context, verb string) (serviceState, error) {
	r, err := h.run(ctx, false, "systemctl", verb, "caddy.service")
	unknown := func() (serviceState, error) {
		return "", fmt.Errorf("unknown Caddy service state from %s: %w", verb, errors.Join(errors.New("service observation failed"), err, ctx.Err()))
	}
	if ctx.Err() != nil || r.Truncated {
		return unknown()
	}
	if err != nil {
		var e *localexec.Error
		if !errors.As(err, &e) || e.Kind != localexec.Failed || e.ExitCode != r.ExitCode || r.ExitCode <= 0 || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return unknown()
		}
	} else if r.ExitCode != 0 {
		return unknown()
	}
	state := serviceState(strings.TrimSpace(r.Stdout))
	known := false
	switch verb {
	case "is-active":
		switch state {
		case "active", "reloading", "refreshing":
			known = r.ExitCode == 0
		case "inactive", "failed", "activating", "deactivating", "maintenance":
			known = r.ExitCode == 3
		}
	case "is-enabled":
		// systemctl documents positive status exits for these unit-file states,
		// rather than a particular code. Only a completed status probe qualifies.
		switch state {
		case "enabled", "enabled-runtime", "alias", "static", "indirect", "generated", "transient":
			known = r.ExitCode == 0
		case "disabled", "linked", "linked-runtime", "masked", "masked-runtime":
			known = r.ExitCode > 0
		case "not-found":
			known = r.ExitCode == 4
		}
	}
	if !known {
		return unknown()
	}
	return state, nil
}
