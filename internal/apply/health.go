package apply

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/target"
)

// HTTPHealth never follows redirects, reads response bodies, or uses ambient
// proxy settings. Routed probes verify TLS normally and cover every app domain.
// An injected transport must honor request contexts and be safe for concurrent use.
type HTTPHealth struct {
	Transport    http.RoundTripper
	PollInterval time.Duration
}

func (h HTTPHealth) Check(ctx context.Context, d policy.Desired, port target.Port, routed bool) error {
	if d.Health.StartupDeadlineSeconds < 1 || d.Health.TimeoutSeconds < 1 || d.Health.ExpectedStatus < 100 || d.Health.ExpectedStatus > 599 || port < 1024 || port > 65535 {
		return errors.New("apply: invalid health configuration")
	}
	path, err := url.ParseRequestURI(string(d.Health.Path))
	if err != nil || !strings.HasPrefix(string(d.Health.Path), "/") || path.Host != "" || path.Scheme != "" {
		return errors.New("apply: invalid health path")
	}
	endpoints := []string{"http://127.0.0.1:" + strconv.FormatUint(uint64(port), 10) + string(d.Health.Path)}
	if routed {
		if len(d.Domains) == 0 {
			return errors.New("apply: routed health requires domains")
		}
		endpoints = make([]string, 0, len(d.Domains))
		for _, domain := range d.Domains {
			// Desired was verified at the executor boundary. Refuse URL-shaped casts
			// here too so standalone probes cannot redirect credentials or reach a path.
			if strings.ContainsAny(string(domain), "/:@?#\\") || domain == "" {
				return errors.New("apply: invalid health domain")
			}
			endpoints = append(endpoints, "https://"+string(domain)+string(d.Health.Path))
		}
	}
	transport := h.Transport
	if transport == nil {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.Proxy = nil
		defer t.CloseIdleConnections()
		transport = t
	}
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(d.Health.StartupDeadlineSeconds)*time.Second)
	defer cancel()
	interval := h.PollInterval
	if interval <= 0 {
		interval = 250 * time.Millisecond
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		healthy := true
		for _, endpoint := range endpoints {
			probeCtx, probeCancel := context.WithTimeout(ctx, time.Duration(d.Health.TimeoutSeconds)*time.Second)
			req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, endpoint, nil)
			if err != nil {
				probeCancel()
				return errors.New("apply: invalid health request")
			}
			response, err := client.Do(req)
			if err != nil {
				healthy = false
			} else {
				healthy = healthy && response.StatusCode == d.Health.ExpectedStatus
				response.Body.Close()
			}
			probeCancel()
			if !healthy {
				break
			}
		}
		if healthy {
			return nil
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
