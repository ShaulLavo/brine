package apply

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spec"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestHTTPHealthEndpointsAndRetries(t *testing.T) {
	d := policy.Desired{Health: policy.Health{Path: "/ready", ExpectedStatus: 204, StartupDeadlineSeconds: 1, TimeoutSeconds: 1}, Domains: []spec.Domain{"hello.example.com", "other.example.com"}}
	for _, routed := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "routed"}[routed], func(t *testing.T) {
			var urls []string
			h := HTTPHealth{PollInterval: time.Millisecond, Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
				urls = append(urls, r.URL.String())
				status := 204
				if len(urls) == 1 {
					status = 503
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("not logged")), Header: make(http.Header)}, nil
			})}
			if err := h.Check(context.Background(), d, 20000, routed); err != nil {
				t.Fatal(err)
			}
			if len(urls) < 2 {
				t.Fatal("did not retry")
			}
			want := "http://127.0.0.1:20000/ready"
			if routed {
				want = "https://other.example.com/ready"
			}
			if urls[len(urls)-1] != want {
				t.Fatal(urls)
			}
		})
	}
}
func TestHTTPHealthDeadlineAndNoRedirect(t *testing.T) {
	d := policy.Desired{Health: policy.Health{Path: "/", ExpectedStatus: 200, StartupDeadlineSeconds: 1, TimeoutSeconds: 1}}
	calls := 0
	h := HTTPHealth{PollInterval: time.Millisecond, Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != "http://127.0.0.1:20000/" {
			t.Errorf("followed redirect to %s", r.URL)
		}
		return &http.Response{StatusCode: 302, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{"Location": []string{"https://unowned.example.com/"}}}, nil
	})}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := h.Check(ctx, d, 20000, false); err != context.DeadlineExceeded {
		t.Fatalf("%v", err)
	}
	if calls == 0 {
		t.Fatal("no probe")
	}
}

func TestRoutedHealthDialsLocalCaddyWithDomainHostAndSNI(t *testing.T) {
	for _, port := range []uint16{0, 9443} {
		t.Run(strconv.Itoa(int(port)), func(t *testing.T) { testRoutedLocalCaddy(t, port, false) })
	}
}
func TestHealthyOtherDestinationCannotSatisfyLocalRoutedHealth(t *testing.T) {
	testRoutedLocalCaddy(t, 443, true)
}
func testRoutedLocalCaddy(t *testing.T, port uint16, localUnavailable bool) {
	const domain = "hello.example.com"
	public, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{domain}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, public, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	observations := make(chan [3]string, 1)
	var localAttempts atomic.Int32
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "wrong.example.com"}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		if localUnavailable && strings.HasPrefix(address, "127.0.0.1:") {
			localAttempts.Add(1)
			return nil, errors.New("local listener unavailable")
		}
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			secured := tls.Server(server, &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}})
			request, err := http.ReadRequest(bufio.NewReader(secured))
			if err != nil {
				observations <- [3]string{address, "error", err.Error()}
				return
			}
			observations <- [3]string{address, request.Host, secured.ConnectionState().ServerName}
			_, _ = io.WriteString(secured, "HTTP/1.1 204 No Content\r\nConnection: close\r\n\r\n")
		}()
		return client, nil
	}}
	h := HTTPHealth{Transport: transport, CaddyPort: port}
	d := policy.Desired{Health: policy.Health{Path: "/ready", ExpectedStatus: 204, StartupDeadlineSeconds: 1, TimeoutSeconds: 1}, Domains: []spec.Domain{domain}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if localUnavailable {
		localCtx, localCancel := context.WithTimeout(ctx, 10*time.Millisecond)
		defer localCancel()
		if err := h.Check(localCtx, d, 20000, true); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("healthy other destination satisfied failed local listener: %v", err)
		}
		if localAttempts.Load() == 0 {
			t.Fatal("local listener was not checked")
		}
		return
	}
	if err := h.Check(ctx, d, 20000, true); err != nil {
		t.Fatal(err)
	}
	if transport.TLSClientConfig.ServerName != "wrong.example.com" {
		t.Fatal("mutated injected transport")
	}
	got := <-observations
	if port == 0 {
		port = 443
	}
	if want := ([3]string{net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))), domain, domain}); got != want {
		t.Fatalf("dial, Host, SNI = %v; want %v", got, want)
	}
}
