package caddy

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/spec"
)

const managedLogProbeToken = "NON_SECRET_TOKEN_PROBE_QUERY"

func managedLogEncoder(field string) string {
	return "format filter {\n wrap json\n fields {\n request>uri " + field + "\n }\n }\n"
}

func managedLoggingProbeConfig(t *testing.T, address, upstream, global, site, shadow string) []byte {
	t.Helper()
	raw, err := RenderManagedPreview(managedPreview(t, true))
	if err != nil {
		t.Fatal(err)
	}
	_, body, _ := strings.Cut(string(raw), "\n")
	_, body, _ = strings.Cut(body, "\n")
	body = strings.NewReplacer("127.0.0.1:20000", upstream, "127.0.0.1:20001", upstream).Replace(body)
	return []byte("{\n admin off\n auto_https off\n" + global + shadow + "}\nhttp://" + address + " {\n" + site + body)
}

func managedLoggingProbeAddress(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return listener
}

func managedLoggingProbe(t *testing.T, binary string, config []byte, address string) []byte {
	t.Helper()
	directory := t.TempDir()
	file := filepath.Join(directory, "Caddyfile")
	if err := os.WriteFile(file, config, 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	output, err := root.OpenFile("process.jsonl", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = output.Close() }()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	command := caddyVerifierCommand(t, ctx, binary, "run", "--config", file, "--adapter", "caddyfile")
	command.Stdout, command.Stderr = output, output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); _ = command.Wait() }()
	waitManagedLogProbe(t, address)
	paths := []string{
		"/_brine/db/main/v1/database/game/subscribe?token=" + managedLogProbeToken,
		"/_brine/db/main/v1/database/game/subscribe?%74oken=" + managedLogProbeToken,
		"/_brine/db/main/v1/database/game/%73ubscribe?token=" + managedLogProbeToken,
		"/_brine/db/main/v1/database/game/subscribe?token=" + managedLogProbeToken + "&flag=%ZZ",
		"//hostname:bad/_brine/db/main/v1/database/game/subscribe?token=" + managedLogProbeToken,
	}
	for _, path := range paths {
		sendManagedLogProbe(t, address, path)
	}
	cancel()
	_ = command.Wait()
	if _, err := output.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	logs, err := io.ReadAll(io.LimitReader(output, 1<<20))
	if err != nil || len(logs) == 1<<20 {
		t.Fatal("bounded Caddy log read failed", err)
	}
	return logs
}

func waitManagedLogProbe(t *testing.T, address string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		connection, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if err == nil {
			_ = connection.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("bounded Caddy logging probe did not listen")
}

func sendManagedLogProbe(t *testing.T, address, path string) {
	t.Helper()
	connection, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = connection.Close() }()
	if err := connection.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	request := "GET " + path + " HTTP/1.1\r\nHost: " + address + "\r\nUpgrade: websocket\r\nAuthorization: Bearer NON_SECRET_TOKEN_PROBE_HEADER\r\nConnection: close\r\n\r\n"
	if _, err := io.WriteString(connection, request); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(connection), nil)
	if err != nil {
		t.Fatal("Caddy logging probe response failed", err)
	}
	defer func() { _ = response.Body.Close() }()
	if _, err := io.Copy(io.Discard, io.LimitReader(response.Body, 4096)); err != nil {
		t.Fatal(err)
	}
}

func TestManagedLoggingAdmissionAndLogsWithPinnedCaddy(t *testing.T) {
	binary := pinnedCaddyVerifier(t)
	deleteEncoder := managedLogEncoder("delete")
	queryEncoder := managedLogEncoder("query {\n delete token\n }")
	cases := []struct {
		name, global, site, shadow string
		admitted, leaks            bool
	}{
		{"delete URI everywhere", "log default {\n" + deleteEncoder + "}\n", "log {\n" + deleteEncoder + "}\n", "", true, false},
		{"site only deletion", "", "log {\n" + deleteEncoder + "}\n", "", false, true},
		{"query deletion", "log default {\n" + queryEncoder + "}\n", "log {\n" + queryEncoder + "}\n", "", false, true},
		{"explicit discard", "log default {\n output discard\n }\n", "log {\n output discard\n }\n", "", true, false},
		{"error shadow", "log default {\n" + deleteEncoder + "}\n", "log {\n" + deleteEncoder + "}\n", "log shadow {\n include http.log.error\n format json\n}\n", false, true},
		{"access shadow", "log default {\n" + deleteEncoder + "}\n", "log {\n" + deleteEncoder + "}\n", "log shadow {\n include http.log.access\n format json\n}\n", false, true},
		{"proxy debug shadow", "log default {\n" + deleteEncoder + "}\n", "log {\n" + deleteEncoder + "}\n", "log shadow {\n include http.handlers.reverse_proxy\n level DEBUG\n format json\n}\n", false, true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			checkManagedLoggingProbe(t, binary, test.global, test.site, test.shadow, test.admitted, test.leaks)
		})
	}
}

func checkManagedLoggingProbe(t *testing.T, binary, global, site, shadow string, admitted, leaks bool) {
	t.Helper()
	upstream := managedLoggingProbeAddress(t)
	listener := managedLoggingProbeAddress(t)
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	config := managedLoggingProbeConfig(t, address, upstream.Addr().String(), global, site, shadow)
	file := filepath.Join(t.TempDir(), "Caddyfile")
	if err := os.WriteFile(file, config, 0o600); err != nil {
		t.Fatal(err)
	}
	adapted := runCaddyVerifier(t, binary, "adapt", "--config", file, "--adapter", "caddyfile")
	if got := VerifyManagedLogging(adapted, []spec.Domain{"127.0.0.1"}) == nil; got != admitted {
		t.Fatalf("actual Caddy admission=%t want=%t", got, admitted)
	}
	if err := upstream.Close(); err != nil {
		t.Fatal(err)
	}
	logs := managedLoggingProbe(t, binary, config, address)
	if got := bytes.Contains(logs, []byte(managedLogProbeToken)); got != leaks {
		t.Fatalf("actual query-token logging=%t want=%t", got, leaks)
	}
	if bytes.Contains(logs, []byte("NON_SECRET_TOKEN_PROBE_HEADER")) {
		t.Fatal("Caddy exposed the bearer-header sentinel")
	}
}
