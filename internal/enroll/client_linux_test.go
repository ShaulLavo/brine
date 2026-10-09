package enroll

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/target"
	"github.com/ShaulLavo/brine/internal/transport"
)

type operatorFake struct {
	facts          Facts
	commands       []localexec.Command
	refuseProbe    bool
	failAction     string
	failed         bool
	restrictions   int
	retainedPolicy bool
}

func (f *operatorFake) RunInput(_ context.Context, c localexec.Command) (localexec.Output, error) {
	f.commands = append(f.commands, c)
	var data []byte
	var err error
	if c.Path == "scp" {
		return localexec.Output{}, nil
	}
	remote := c.Args[len(c.Args)-1]
	switch {
	case remote == "uname -m; id -u":
		arch := "x86_64"
		if runtime.GOARCH == "arm64" {
			arch = "aarch64"
		}
		data = []byte(arch + "\n0\n")
	case remote == "mktemp -d /tmp/brine-enroll.XXXXXXXX":
		data = []byte("/tmp/brine-enroll.ABCDEFGH\n")
	case strings.HasPrefix(remote, "rm -- /tmp/brine-enroll."):
		return localexec.Output{}, nil
	case strings.HasSuffix(remote, "/brine host enrollment"):
		var request HostRequest
		if err = json.Unmarshal(c.Stdin, &request); err != nil {
			return localexec.Output{}, err
		}
		if request.Action == "probe" && f.refuseProbe {
			return localexec.Output{}, errors.New("unsafe SSH environment")
		}
		if request.Action == "apply" {
			var raw map[string]json.RawMessage
			_ = json.Unmarshal(c.Stdin, &raw)
			var binding string
			_ = json.Unmarshal(raw["confirmed"], &binding)
			if binding == "" {
				return localexec.Output{}, errors.New("apply missing confirmed transaction binding")
			}

			f.facts.OwnedRunner = true
			f.facts.Snapshot.Runner.User = target.Known("brine")
			f.facts.Snapshot.Runner.Linger = target.Known(true)
		}
		if request.Action == f.failAction && !f.failed {
			f.failed = true
			return localexec.Output{}, errors.New("injected unknown outcome")
		}
		if request.Action == "undo" {
			f.facts.OwnedRunner = false
			f.facts.Snapshot.Runner.User = target.Observation[string]{Status: target.Absent}
		}
		if request.Action == "undo" {
			data, err = json.Marshal(UndoResult{Removed: true, RetainedPolicy: f.retainedPolicy})
		} else {
			data, err = json.Marshal(f.facts)
		}
	case remote == "brine host serve" || remote == "printf brine-unrestricted-key":
		if remote == "printf brine-unrestricted-key" {
			f.restrictions++
		}
		var request dispatch.Request
		_ = json.Unmarshal(c.Stdin, &request)
		if request.Op == "inventory" {
			fixture, e := os.ReadFile("../target/testdata/ready-arm64.json")
			if e != nil {
				return localexec.Output{}, e
			}
			snapshot, e := target.Decode(fixture)
			if e != nil {
				return localexec.Output{}, e
			}
			data, err = json.Marshal(result.Success("brine host inventory", snapshot))
		} else {
			data, err = json.Marshal(result.Success("brine host ping", dispatch.PingData{ServerVersion: "fixture", ProtocolVersions: []int{1}}))
		}
	default:
		return localexec.Output{}, errors.New("unexpected fake command")
	}
	return localexec.Output{Stdout: data}, err
}
func fakeClientOptions(t *testing.T) (Options, *operatorFake) {
	t.Helper()
	if runtime.GOARCH != "arm64" && runtime.GOARCH != "amd64" {
		t.Skip("supported Linux binary architecture required")
	}
	dir := t.TempDir()
	public := filepath.Join(dir, "deploy.pub")
	private := filepath.Join(dir, "deploy")
	blob := append([]byte{0, 0, 0, 11}, []byte("ssh-ed25519")...)
	blob = append(blob, 0, 0, 0, 32)
	blob = append(blob, make([]byte, 32)...)
	key := "ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob)
	if err := os.WriteFile(public, []byte(key+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(private, []byte("synthetic private fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	f := &operatorFake{facts: supported()}
	f.facts.HostKey = key
	return Options{Destination: "operator@fixture", Name: "fixture", DeployKeyPath: public, HostBinary: binary, ConfigDir: filepath.Join(dir, "targets")}, f
}
func TestClientEnrollmentAndUndoWithOperatorSSH(t *testing.T) {
	o, f := fakeClientOptions(t)
	var output bytes.Buffer
	client := Client{Runner: f, Input: strings.NewReader("fixture\n"), Output: &output, Terminal: true}
	if err := client.Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(o.ConfigDir, "fixture.json")
	pin, err := transport.LoadTarget(config)
	if err != nil || pin.Destination != "brine@fixture" {
		t.Fatal("target not pinned", err)
	}
	if f.restrictions != 1 {
		t.Fatal("restriction was not checked")
	}
	for _, cmd := range f.commands {
		if len(cmd.Args) == 0 {
			t.Fatal("untyped subprocess")
		}
		if cmd.Path == "ssh" && cmd.Args[len(cmd.Args)-1] != "brine host serve" && cmd.Args[len(cmd.Args)-1] != "printf brine-unrestricted-key" {
			if !strings.Contains(strings.Join(cmd.Args, " "), "operator@fixture") {
				t.Fatal("admin access used deploy account")
			}
		}
	}
	f.refuseProbe = true
	f.retainedPolicy = true
	o.Undo = true
	client.Input = strings.NewReader("fixture\n")
	if err = client.Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Operator-edited policy retained at /etc/ssh/brine/operator-policy.toml.") {
		t.Fatal("missing retained-policy report")
	}
	if _, err = os.Stat(config); !errors.Is(err, os.ErrNotExist) || f.facts.OwnedRunner {
		t.Fatal("undo leaked target")
	}
}
func TestClientFailureAfterApplyReconcilesOnRerun(t *testing.T) {
	for _, action := range []string{"apply", "verify"} {
		o, f := fakeClientOptions(t)
		f.failAction = action
		var output bytes.Buffer
		c := Client{Runner: f, Input: strings.NewReader("fixture\n"), Output: &output, Terminal: true}
		if c.Run(context.Background(), o) == nil {
			t.Fatal("injected failure missing")
		}
		if _, err := os.Stat(filepath.Join(o.ConfigDir, "fixture.json")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("success target written before verification")
		}
		c.Input = strings.NewReader("fixture\n")
		if err := c.Run(context.Background(), o); err != nil {
			t.Fatal(err)
		}
	}
}
func TestNonterminalClientNeverReachesSSH(t *testing.T) {
	o, f := fakeClientOptions(t)
	c := Client{Runner: f, Input: strings.NewReader("fixture\n"), Output: &bytes.Buffer{}}
	if c.Run(context.Background(), o) == nil || len(f.commands) != 0 {
		t.Fatal("agent input reached admin SSH")
	}
}
