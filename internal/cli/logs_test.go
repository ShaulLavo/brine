package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/logs"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/transport"
)

type logsCaller struct {
	requests []dispatch.Request
	lines    []logs.Line
	err      error
}

func (f *logsCaller) Call(_ context.Context, _ transport.Target, r dispatch.Request) (result.Envelope, error) {
	f.requests = append(f.requests, r)
	if f.err != nil {
		return result.Envelope{}, f.err
	}
	return result.Success("brine host logs", f.lines), nil
}
func targetConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	blob := make([]byte, 51)
	binary.BigEndian.PutUint32(blob[:4], 11)
	copy(blob[4:15], "ssh-ed25519")
	binary.BigEndian.PutUint32(blob[15:19], 32)
	cfg := transport.Target{Name: "fixture", Destination: "brine@fixture.test", IdentityPath: "/fixture/key", PinnedHostKey: "ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob)}
	raw, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(dir, "fixture.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}
func TestLogsModes(t *testing.T) {
	for _, mode := range []string{"human", "--json", "--jsonl"} {
		var out, diag bytes.Buffer
		client := &logsCaller{lines: []logs.Line{{Timestamp: "2026-10-09T00:00:00Z", Priority: 6, Message: "ready"}, {Timestamp: "2026-10-09T00:00:01Z", Priority: 3, Message: "API_KEY=hidden\x1b[31m\nnext"}}}
		deps := Dependencies{Context: context.Background(), Stdin: strings.NewReader(""), Stdout: &out, Stderr: &diag, LogsClient: client}
		args := []string{"logs", "api", "--target", "fixture", "--tail", "5", "--config-dir", targetConfig(t)}
		if mode != "human" {
			args = append(args, mode)
		}
		if err := Execute(deps, args); err != nil {
			t.Fatal(err)
		}
		if len(client.requests) != 1 || client.requests[0].Op != "logs" {
			t.Fatal("wrong request")
		}
		request, err := logs.DecodeRequest(client.requests[0].Args)
		if err != nil || request.App != "api" || request.Tail != 5 {
			t.Fatalf("%+v %v", request, err)
		}
		if strings.Contains(out.String(), "hidden") || strings.Contains(out.String(), "\x1b") {
			t.Fatal("unsafe output")
		}
		switch mode {
		case "human":
			if !strings.Contains(out.String(), "ready") || strings.Count(out.String(), "\n") != 2 {
				t.Fatal(out.String())
			}
		case "--json":
			var e result.Envelope
			if json.Unmarshal(out.Bytes(), &e) != nil || !e.OK || e.Command != "brine logs" {
				t.Fatal(out.String())
			}
		case "--jsonl":
			lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
			if len(lines) != 3 {
				t.Fatalf("wrong frame count %s", out.String())
			}
			for i, line := range lines {
				var e result.Envelope
				if json.Unmarshal([]byte(line), &e) != nil || !e.OK || e.Command != "brine logs" {
					t.Fatal(line)
				}
				data := e.Data.(map[string]any)
				if i < 2 && data["event"] != "log" {
					t.Fatal(line)
				}
				if i == 2 && data["event"] != "complete" {
					t.Fatal(line)
				}
			}
		}
	}
}
func TestLogsFailuresAndEmpty(t *testing.T) {
	for _, tt := range []struct {
		tail   string
		app    string
		err    error
		frames int
	}{
		{"5", "api", nil, 1}, {"5", "api", result.New(result.LogsOwnershipRefused, nil), 1}, {"1001", "api", nil, 1}, {"5", "api;id", nil, 1},
	} {
		var out, diag bytes.Buffer
		client := &logsCaller{lines: []logs.Line{}, err: tt.err}
		err := Execute(Dependencies{Context: context.Background(), Stdin: strings.NewReader(""), Stdout: &out, Stderr: &diag, LogsClient: client}, []string{"logs", tt.app, "--target", "fixture", "--tail", tt.tail, "--config-dir", targetConfig(t), "--jsonl"})
		if (tt.err != nil || tt.tail == "1001" || tt.app == "api;id") && err == nil {
			t.Fatal("accepted invalid logs")
		}
		if strings.Count(out.String(), "\n") != tt.frames || !json.Valid(bytes.TrimSpace(out.Bytes())) {
			t.Fatal(out.String())
		}
	}
}
