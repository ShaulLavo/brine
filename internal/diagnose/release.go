package diagnose

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/target"
)

func (r Reader) currentApp(ctx context.Context, snapshot target.Snapshot, udpState *plan.UDPState, current ops.Release, unit string, a *App) {
	stored := fact(ctx, func(ctx context.Context) (releasePlan, error) {
		p, d, err := r.Store.LoadPlan(ctx, current.PlanID)
		return releasePlan{Plan: p, Desired: d}, err
	})
	if stored.Value == nil {
		a.Health = unknown[bool](stored.Reason)
		a.Drift = drift(snapshot, a.Name, current, unknown[policy.Desired](stored.Reason), nil)
		return
	}
	p := stored.Value.Plan
	desired := stored.Value.Desired
	a.UDP = diagnosticUDP(snapshot, udpState, a.Name, p, current.Units)
	a.UDPRemnants = retiredUDPRemnants(snapshot, p)
	var allocation *plan.UDPAllocation
	if p.UDP != nil {
		allocation = p.UDP.Next
	}
	a.Drift = drift(snapshot, a.Name, current, Known(desired), allocation)
	if unit == "" {
		return
	}
	a.Health = fact(ctx, func(ctx context.Context) (bool, error) { return committedHealth(ctx, current, desired) })
}

func committedHealth(ctx context.Context, current ops.Release, desired policy.Desired) (bool, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	client := http.Client{Transport: transport, Timeout: ProbeTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	url := fmt.Sprintf("http://127.0.0.1:%d%s", current.HostPort, desired.Health.Path)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, err
	}
	response, err := client.Do(request)
	if err != nil {
		return false, err
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	return response.StatusCode == desired.Health.ExpectedStatus, nil
}
