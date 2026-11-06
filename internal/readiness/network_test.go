package readiness

import (
	"context"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ShaulLavo/brine/internal/target"
)

func fixtureNetwork(t *testing.T, handler http.Handler) (NetworkProbes, *httptest.Server) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	return NetworkProbes{roots: roots, dial: dial}, server
}
func TestReadinessRegistryHTTPBoundary(t *testing.T) {
	for _, code := range []int{200, 401, 403, 500, 301, 302, 307, 308} {
		t.Run(http.StatusText(code), func(t *testing.T) { testRegistryHTTP(t, code) })
	}
}
func testRegistryHTTP(t *testing.T, code int) {
	t.Helper()
	var calls atomic.Int32
	n, _ := fixtureNetwork(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" || r.URL.Path != "/v2/" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("not anonymous fixed-path GET", r)
		}
		w.Header().Set("Location", "https://unrelated.example.com/private")
		w.WriteHeader(code)
	}))
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	ok, err := n.Registry(t.Context(), "example.com")
	if err != nil || ok != (code == 200 || code == 401) || calls.Load() != 1 {
		t.Fatal(ok, err, calls.Load())
	}
}
func TestReadinessTLSVerificationNeverDisabled(t *testing.T) {
	n, server := fixtureNetwork(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	// The httptest certificate includes example.com, not this wrong hostname.
	for _, test := range []struct {
		name    string
		trusted bool
		want    string
	}{{"example.com", true, "trusted"}, {"wrong.invalid", true, "untrusted"}, {"example.com", false, "untrusted"}} {
		candidate := n
		if !test.trusted {
			candidate.roots = x509.NewCertPool()
		}
		got, err := candidate.TLS(t.Context(), test.name)
		if err != nil || got != test.want {
			t.Fatal(test, got, err)
		}
	}
	if !strings.Contains(server.Certificate().DNSNames[0], "example") {
		t.Fatal("fixture certificate changed")
	}
	// Same verification policy is mandatory for registry HTTPS.
	n.roots = x509.NewCertPool()
	if ok, err := n.Registry(t.Context(), "example.com"); ok || err != nil {
		t.Fatal(ok, err)
	}
	n.dial = func(_ context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" || address != "127.0.0.1:443" {
			t.Error("arbitrary TLS destination", network, address)
		}
		return nil, errors.New("planted-secret")
	}
	if got, err := n.TLS(t.Context(), "example.com"); got != "unreachable" || err != nil {
		t.Fatal(got, err)
	}
}
func TestReadinessDNSBoundary(t *testing.T) {
	for _, test := range []struct {
		local  []string
		answer []string
		want   string
	}{
		{[]string{"192.0.2.10"}, []string{"192.0.2.10"}, "resolves_here"},
		{[]string{"192.0.2.10"}, []string{"192.0.2.11"}, "resolves_elsewhere"},
		{[]string{"192.0.2.10"}, []string{"192.0.2.10", "192.0.2.11"}, "resolves_elsewhere"},
		{[]string{"10.0.0.1"}, []string{"192.0.2.10"}, "unknown_behind_nat"},
		{[]string{"2001:db8::1"}, []string{"2001:db8::1"}, "resolves_here"},
	} {
		testDNSLocation(t, test.local, test.answer, test.want)
	}
	n := NetworkProbes{lookup: func(_ context.Context, network, name string) ([]net.IP, error) {
		if network != "ip" || name != "app.example.com" {
			t.Fatal(network, name)
		}
		return nil, &net.DNSError{IsNotFound: true}
	}}
	got, err := n.DNS(t.Context(), "app.example.com", target.Known([]string{}))
	if err != nil || got != "unresolved" {
		t.Fatal(got, err)
	}
	n.lookup = func(context.Context, string, string) ([]net.IP, error) { return nil, errors.New("planted-secret") }
	if _, err := n.DNS(t.Context(), "app.example.com", target.Known([]string{})); !errors.Is(err, errProbe) {
		t.Fatal(err)
	}
	for _, name := range []string{"*.example.com", "https://example.com/path", "example.com/private", "user@example.com"} {
		if _, err := n.Registry(t.Context(), name); err == nil {
			t.Fatal("unsafe host admitted", name)
		}
		if _, err := n.TLS(t.Context(), name); err == nil {
			t.Fatal("unsafe SNI admitted", name)
		}
	}
}
func testDNSLocation(t *testing.T, local, answers []string, want string) {
	t.Helper()
	n := NetworkProbes{lookup: func(context.Context, string, string) ([]net.IP, error) {
		out := []net.IP{}
		for _, text := range answers {
			out = append(out, net.ParseIP(text))
		}
		return out, nil
	}}
	got, err := n.DNS(t.Context(), "app.example.com", target.Known(local))
	if err != nil || got != want {
		t.Fatal(local, answers, got, err)
	}
}

func TestReadinessDockerHubUsesExistingAPIOriginWithoutRedirect(t *testing.T) {
	for _, host := range []string{"docker.io", "index.docker.io"} {
		dialed := ""
		n := NetworkProbes{dial: func(_ context.Context, _, address string) (net.Conn, error) { dialed = address; return nil, errProbe }}
		if reachable, err := n.Registry(t.Context(), host); reachable || err != nil || dialed != "registry-1.docker.io:443" {
			t.Fatal(host, reachable, err, dialed)
		}
	}
}
