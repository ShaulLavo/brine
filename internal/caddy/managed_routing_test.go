package caddy

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type managedRequestCase struct {
	name, method, uri, authorization, upgrade, destination string
}

func routeStub(destination string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Brine-Test-Destination", destination)
		w.WriteHeader(http.StatusOK)
	}))
}

func loopbackCaddy(t *testing.T, binary string, config []byte) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	_, body, _ := strings.Cut(string(config), "\n")
	_, body, _ = strings.Cut(body, "\n")
	config = []byte("{\n admin off\n auto_https off\n}\nhttp://" + address + " {\n" + body)
	file := filepath.Join(t.TempDir(), "Caddyfile")
	if err := os.WriteFile(file, config, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	command := caddyVerifierCommand(t, ctx, binary, "run", "--config", file, "--adapter", "caddyfile")
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if err := command.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = command.Wait() })
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		connection, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if err == nil {
			_ = connection.Close()
			return "http://" + address
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("bounded loopback Caddy verifier did not listen")
	return ""
}

func checkRenderedManagedRoute(t *testing.T, client *http.Client, address string, ingress ClientIngress, test managedRequestCase) {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), test.method, address+test.uri, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", test.authorization)
	request.Header.Set("Upgrade", test.upgrade)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	wantStatus := http.StatusOK
	if test.destination == "" {
		wantStatus = http.StatusForbidden
	}
	if response.StatusCode != wantStatus || response.Header.Get("X-Brine-Test-Destination") != test.destination {
		t.Fatalf("route %s %s returned status %d destination %q, want %d %q", test.method, test.uri, response.StatusCode, response.Header.Get("X-Brine-Test-Destination"), wantStatus, test.destination)
	}
	if ingress.Allows(test.method, test.uri, test.authorization, test.upgrade) != (test.destination == "engine") {
		t.Fatal("pure checker and actual rendered engine route disagree")
	}
}

func TestManagedPreviewRoutingWithPinnedCaddy(t *testing.T) {
	binary := pinnedCaddyVerifier(t)
	engine, frontend := routeStub("engine"), routeStub("frontend")
	t.Cleanup(engine.Close)
	t.Cleanup(frontend.Close)
	preview := managedPreview(t, true)
	raw, err := RenderManagedPreview(preview)
	if err != nil {
		t.Fatal(err)
	}
	replacements := strings.NewReplacer("127.0.0.1:20000", strings.TrimPrefix(engine.URL, "http://"), "127.0.0.1:20001", strings.TrimPrefix(frontend.URL, "http://"))
	address := loopbackCaddy(t, binary, []byte(replacements.Replace(string(raw))))
	ingress, err := NewManagedClientIngress(preview)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{Proxy: nil}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	base := "/_brine/db/main/v1/"
	cases := []managedRequestCase{
		{"subscription", "GET", base + "database/game/subscribe?token=client", "", "websocket", "engine"},
		{"case insensitive upgrade", "GET", base + "database/game/subscribe?token=client", "", "WebSocket", "engine"},
		{"SDK query fields", "GET", base + "database/game/subscribe?confirmed=true&token=client&compression=Gzip&connection_id=fixture", "", "websocket", "engine"},
		{"encoded token key", "GET", base + "database/game/subscribe?%74oken=client", "", "websocket", "engine"},
		{"escaped token value", "GET", base + "database/game/subscribe?token=one%3Btwo", "", "websocket", "engine"},
		{"encoded route", "GET", base + "database/game/%73ubscribe?token=client", "", "websocket", ""},
		{"cleaned route", "GET", base + "database/game/../game/subscribe?token=client", "", "websocket", ""},
		{"duplicate token", "GET", base + "database/game/subscribe?token=one&token=two", "", "websocket", ""},
		{"aliased duplicate token", "GET", base + "database/game/subscribe?token=one&%74oken=two", "", "websocket", ""},
		{"malformed query", "GET", base + "database/game/subscribe?token=one;token=two", "", "websocket", ""},
		{"unrelated malformed query", "GET", base + "database/game/subscribe?token=client&flag=%ZZ", "", "websocket", ""},
		{"management", "GET", base + "database/game/environment", "", "", ""},
		{"another database", "GET", base + "database/other/subscribe?token=client", "", "websocket", ""},
		{"missing token", "GET", base + "database/game/subscribe", "", "websocket", ""},
		{"token exchange", "POST", base + "identity/websocket-token", "Bearer client", "", "engine"},
		{"escaped token exchange", "POST", base + "identity/%77ebsocket-token", "Bearer client", "", ""},
		{"frontend", "GET", "/ordinary", "", "", "frontend"},
		{"reserved traversal", "GET", "/_brine/db/main/../../../ordinary", "", "", ""},
		{"encoded reserved traversal", "GET", "/_brine%2fdb/main/../../../ordinary", "", "", ""},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) { checkRenderedManagedRoute(t, client, address, ingress, test) })
	}
}
