package apps

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
)

func TestDirectHealthDoesNotFollowRedirects(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch r.URL.Path {
		case "/health":
			w.WriteHeader(204)
		case "/redirect":
			http.Redirect(w, r, "/health", http.StatusFound)
		default:
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	_, port, e := net.SplitHostPort(server.Listener.Addr().String())
	if e != nil {
		t.Fatal(e)
	}
	number, e := strconv.Atoi(port)
	if e != nil {
		t.Fatal(e)
	}
	t.Setenv("HTTP_PROXY", "http://invalid.example:9")
	for _, tc := range []struct {
		path    string
		healthy bool
	}{{"/health", true}, {"/redirect", false}, {"/failed", false}} {
		ctx, cancel := context.WithTimeout(context.Background(), DirectProbeTimeout)
		healthy, e := (HTTPProbe{}).Check(ctx, target.Port(number), policy.Health{Path: spec.HealthPath(tc.path), ExpectedStatus: 204})
		cancel()
		if e != nil || healthy != tc.healthy {
			t.Fatal(tc, e, healthy)
		}
	}
	if calls.Load() != 3 {
		t.Fatal("redirect was followed", calls.Load())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := (HTTPProbe{}).Check(ctx, target.Port(number), policy.Health{Path: "/health", ExpectedStatus: 204}); e == nil {
		t.Fatal("canceled request probed health")
	}
}
