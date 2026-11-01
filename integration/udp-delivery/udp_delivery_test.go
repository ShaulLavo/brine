package udpdelivery_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/fixtures/udp-echo/client"
	"github.com/ShaulLavo/brine/fixtures/udp-echo/protocol"
	"github.com/ShaulLavo/brine/fixtures/udp-echo/server"
)

type fixtureStats struct {
	Received  uint64 `json:"received"`
	Replied   uint64 `json:"replied"`
	Rejected  uint64 `json:"rejected"`
	Oversized uint64 `json:"oversized"`
}

func startFixture(t *testing.T, release string) *server.Server {
	t.Helper()
	fixture, err := server.New(t.Context(), server.Config{
		HTTPAddr: "127.0.0.1:0",
		UDPAddr:  "127.0.0.1:0",
		Identity: protocol.Identity{Fixture: protocol.FixtureName, Release: release},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- fixture.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("fixture stop: %v", err)
			}
		case <-time.After(time.Second):
			t.Error("fixture did not stop within one second")
			_ = fixture.Close(t.Context())
		}
	})
	return fixture
}

func TestHTTPHealthAndUDPNonceEcho(t *testing.T) {
	fixture := startFixture(t, "release-a")
	health := getJSON(t, "http://"+fixture.HTTPAddr()+"/healthz")
	if health["ok"] != true || health["release"] != "release-a" || health["fixture"] != protocol.FixtureName {
		t.Fatalf("unexpected health: %v", health)
	}
	result, err := client.Probe(t.Context(), client.Config{
		Endpoint:        fixture.UDPAddr(),
		Nonce:           "nonce-a",
		ExpectedRelease: "release-a",
		Attempts:        2,
		AttemptTimeout:  200 * time.Millisecond,
		OverallTimeout:  time.Second,
		Vantage:         "local-loopback",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "ok" || result.Response == nil || result.Response.Nonce != "nonce-a" {
		t.Fatalf("unexpected probe: %+v", result)
	}
	if result.Vantage != "local-loopback" || result.StartedAt.IsZero() || result.FinishedAt.IsZero() {
		t.Fatalf("probe did not retain independent evidence: %+v", result)
	}
}

func TestUDPRejectsInvalidAndOversizedPackets(t *testing.T) {
	fixture := startFixture(t, "release-a")
	connection, err := net.DialUDP("udp", nil, mustUDPAddr(t, fixture.UDPAddr()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = connection.Close() }()
	if err := connection.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Write([]byte("bad\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Write(make([]byte, protocol.MaxDatagramBytes+1)); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Read(make([]byte, protocol.MaxDatagramBytes+1)); err == nil {
		t.Fatal("rejected packet received a response")
	}
	stats := waitForStats(t, fixture, func(stats fixtureStats) bool { return stats.Rejected >= 1 && stats.Oversized >= 1 })
	if stats.Received < 2 {
		t.Fatalf("invalid packets not counted: %+v", stats)
	}
}

func TestClientTimeoutHonorsAttemptsAndOverallDeadline(t *testing.T) {
	silent, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = silent.Close() }()
	started := time.Now()
	result, err := client.Probe(t.Context(), client.Config{
		Endpoint:        silent.LocalAddr().String(),
		Nonce:           "nonce-timeout",
		ExpectedRelease: "release-a",
		Attempts:        protocol.MaxAttempts,
		AttemptTimeout:  100 * time.Millisecond,
		OverallTimeout:  220 * time.Millisecond,
		Vantage:         "local-loopback",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "timeout" || result.Attempts != protocol.MaxAttempts {
		t.Fatalf("unexpected timeout result: %+v", result)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("probe exceeded bound: %s", elapsed)
	}
}

func TestClientRecordsRefusalAndReleaseMismatch(t *testing.T) {
	unused, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := unused.LocalAddr().String()
	_ = unused.Close()
	result, err := client.Probe(t.Context(), client.Config{Endpoint: endpoint, Nonce: "nonce-refused", ExpectedRelease: "release-a", Attempts: 1, AttemptTimeout: 200 * time.Millisecond, OverallTimeout: time.Second, Vantage: "local-loopback"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "refused" && result.Outcome != "timeout" {
		t.Fatalf("unexpected unreachable result: %+v", result)
	}

	fixture := startFixture(t, "release-a")
	result, err = client.Probe(t.Context(), client.Config{Endpoint: fixture.UDPAddr(), Nonce: "nonce-mismatch", ExpectedRelease: "release-b", Attempts: 1, AttemptTimeout: 200 * time.Millisecond, OverallTimeout: time.Second, Vantage: "local-loopback"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "protocol_error" || !strings.Contains(result.Error, "response release mismatch") {
		t.Fatalf("unexpected mismatch result: %+v", result)
	}
}

func TestFixtureShutdownReleasesListeners(t *testing.T) {
	fixture := startFixture(t, "release-a")
	httpAddr := fixture.HTTPAddr()
	udpAddr := mustUDPAddr(t, fixture.UDPAddr())
	if err := fixture.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitForTCPFree(t, httpAddr)
	waitForUDPFree(t, udpAddr)
}

func waitForTCPFree(t *testing.T, endpoint string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		listener, err := net.Listen("tcp", endpoint)
		if err == nil {
			_ = listener.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("HTTP listener retained at %s", endpoint)
}

func waitForUDPFree(t *testing.T, endpoint *net.UDPAddr) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		listener, err := net.ListenUDP("udp", endpoint)
		if err == nil {
			_ = listener.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("UDP listener retained at %s", endpoint)
}

func getJSON(t *testing.T, endpoint string) map[string]any {
	t.Helper()
	client := &http.Client{Timeout: time.Second}
	response, err := client.Get(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("HTTP status: %d", response.StatusCode)
	}
	var value map[string]any
	if err := json.NewDecoder(io.LimitReader(response.Body, protocol.MaxHTTPBodyBytes)).Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func waitForStats(t *testing.T, fixture *server.Server, ready func(fixtureStats) bool) fixtureStats {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		value := getJSON(t, "http://"+fixture.HTTPAddr()+"/stats")
		stats := fixtureStats{
			Received:  uint64(value["stats"].(map[string]any)["received"].(float64)),
			Replied:   uint64(value["stats"].(map[string]any)["replied"].(float64)),
			Rejected:  uint64(value["stats"].(map[string]any)["rejected"].(float64)),
			Oversized: uint64(value["stats"].(map[string]any)["oversized"].(float64)),
		}
		if ready(stats) {
			return stats
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("stats did not reach expected values")
	return fixtureStats{}
}

func mustUDPAddr(t *testing.T, endpoint string) *net.UDPAddr {
	t.Helper()
	address, err := net.ResolveUDPAddr("udp", endpoint)
	if err != nil {
		t.Fatal(err)
	}
	return address
}

func TestHTTPRejectsInvalidAndOversizedRequests(t *testing.T) {
	fixture := startFixture(t, "release-a")
	cases := []struct {
		name   string
		path   string
		body   string
		header string
		status int
	}{
		{name: "body", path: "/healthz", body: "unexpected", status: http.StatusBadRequest},
		{name: "query", path: "/healthz?x=y", status: http.StatusBadRequest},
		{name: "oversized body", path: "/healthz", body: strings.Repeat("a", protocol.MaxHTTPBodyBytes+1), status: http.StatusRequestEntityTooLarge},
		{name: "chunked oversized body", path: "/healthz", body: strings.Repeat("a", protocol.MaxHTTPBodyBytes+1), status: http.StatusRequestEntityTooLarge},
		{name: "oversized header", path: "/healthz", header: strings.Repeat("a", protocol.MaxHTTPHeaderBytes+1), status: http.StatusRequestHeaderFieldsTooLarge},
		{name: "missing", path: "/missing", status: http.StatusNotFound},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			checkHTTPRefusal(t, fixture.HTTPAddr(), test.path, test.body, test.header, test.name == "chunked oversized body", test.status)
		})
	}
}

func checkHTTPRefusal(t *testing.T, address, path, body, header string, chunked bool, expected int) {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+address+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if chunked {
		request.ContentLength = -1
	}
	request.Header.Set("X-Fixture", header)
	httpClient := &http.Client{Timeout: 3 * time.Second}
	response, err := httpClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != expected {
		t.Fatalf("HTTP status %d, expected %d", response.StatusCode, expected)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, protocol.MaxHTTPBodyBytes+1))
	if err != nil || len(data) > protocol.MaxHTTPBodyBytes {
		t.Fatalf("response exceeds bound: %d %v", len(data), err)
	}
}

func TestClientRejectsWrongNonceAndOversizedReplies(t *testing.T) {
	valid, err := protocol.EncodeResponse(protocol.Identity{Fixture: protocol.FixtureName, Release: "release-a"}, []byte("wrong-nonce"))
	if err != nil {
		t.Fatal(err)
	}
	for _, response := range [][]byte{valid, []byte(strings.Repeat("a", protocol.MaxResponseBytes+1)), []byte(`{"unknown":true}`)} {
		checkBadReply(t, response)
	}
}

func checkBadReply(t *testing.T, response []byte) {
	t.Helper()
	listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	defer func() { _ = listener.Close() }()
	go func() { done <- sendReply(listener, response) }()
	result, err := client.Probe(t.Context(), client.Config{Endpoint: listener.LocalAddr().String(), Nonce: "expected-nonce", ExpectedRelease: "release-a", Attempts: 1, AttemptTimeout: time.Second, OverallTimeout: time.Second, Vantage: "local-loopback"})
	if err != nil || result.Outcome != "protocol_error" {
		t.Fatalf("invalid reply accepted: %+v %v", result, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func sendReply(listener *net.UDPConn, response []byte) error {
	if err := listener.SetDeadline(time.Now().Add(time.Second)); err != nil {
		return err
	}
	var request [protocol.MaxDatagramBytes]byte
	_, remote, err := listener.ReadFromUDP(request[:])
	if err != nil {
		return err
	}
	_, err = listener.WriteToUDP(response, remote)
	return err
}

func TestCanceledClientSendsNoPacket(t *testing.T) {
	listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err := client.Probe(ctx, client.Config{Endpoint: listener.LocalAddr().String(), Nonce: "nonce", ExpectedRelease: "release-a", Attempts: 1, AttemptTimeout: time.Second, OverallTimeout: time.Second, Vantage: "local-loopback"})
	if err != nil || result.Outcome != "canceled" || result.Attempts != 0 {
		t.Fatalf("cancellation ignored: %+v %v", result, err)
	}
}

func TestClientCancellationInterruptsRead(t *testing.T) {
	listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	timer := time.AfterFunc(30*time.Millisecond, cancel)
	defer timer.Stop()
	started := time.Now()
	result, err := client.Probe(ctx, client.Config{Endpoint: listener.LocalAddr().String(), Nonce: "nonce", ExpectedRelease: "release-a", Attempts: 1, AttemptTimeout: 5 * time.Second, OverallTimeout: 5 * time.Second, Vantage: "local-loopback"})
	if err != nil || result.Outcome != "canceled" || time.Since(started) > time.Second {
		t.Fatalf("read not canceled within bound: %+v %v", result, err)
	}
}

func TestHTTPIncompleteHeadersHaveDeadline(t *testing.T) {
	fixture := startFixture(t, "release-a")
	connection, err := net.DialTimeout("tcp", fixture.HTTPAddr(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = connection.Close() }()
	if err := connection.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(connection, "GET /healthz HTTP/1.1\r\nHost: fixture\r\n"); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	var response [1]byte
	if _, err := connection.Read(response[:]); err == nil {
		t.Fatal("incomplete headers received a fixture response")
	}
	if time.Since(started) >= 3*time.Second {
		t.Fatal("server did not close incomplete headers within its deadline")
	}
}
