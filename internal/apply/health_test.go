package apply

import (
	"context"
	"io"
	"net/http"
	"strings"
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
