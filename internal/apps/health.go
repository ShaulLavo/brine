package apps

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/target"
)

const DirectProbeLimit = 3
const DirectProbeTimeout = 500 * time.Millisecond

type HealthProbe interface {
	Check(context.Context, target.Port, policy.Health) (bool, error)
}
type HTTPProbe struct{}

func (HTTPProbe) Check(ctx context.Context, port target.Port, health policy.Health) (bool, error) {
	// Never follow app redirects, use a proxy, send credentials, or consume an
	// unbounded body. Only the committed loopback listener may be probed.
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := http.Client{Transport: transport, Timeout: DirectProbeTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d%s", port, health.Path), nil)
	if err != nil {
		return false, err
	}
	response, err := client.Do(request)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	return response.StatusCode == health.ExpectedStatus, nil
}
