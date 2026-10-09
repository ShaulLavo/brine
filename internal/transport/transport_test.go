package transport

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/result"
)

func fakeKey() string {
	b := make([]byte, 51)
	binary.BigEndian.PutUint32(b, 11)
	copy(b[4:], "ssh-ed25519")
	binary.BigEndian.PutUint32(b[15:], 32)
	for i := 19; i < len(b); i++ {
		b[i] = byte(i)
	}
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(b)
}
func validTarget() Target {
	return Target{Name: "fixture", Destination: "runner@fixture.invalid", IdentityPath: "/fixture/identity", PinnedHostKey: fakeKey()}
}
func TestTargetRefusesInjection(t *testing.T) {
	for _, bad := range []string{"-oProxyCommand=touch", "-fixture", "fixture -oForwardAgent=yes", "root@fixture.invalid", "runner@fixture.invalid;touch", "runner@fixture.invalid -oForwardAgent=yes", "ssh://runner@fixture.invalid", "runner@fixture.invalid:22", "runner@fixture.invalid\n", "a@b@fixture.invalid", "runner@-fixture.invalid"} {
		target := validTarget()
		target.Destination = bad
		if err := target.Validate(); result.ExitCode(err) != 2 {
			t.Fatalf("accepted %q: %v", bad, err)
		}
	}
	for _, bad := range []string{"relative", "/fixture/%h", "/fixture/key\n", "/fixture/key with space", "/fixture/\"key"} {
		target := validTarget()
		target.IdentityPath = bad
		if target.Validate() == nil {
			t.Fatalf("accepted path %q", bad)
		}
	}
	target := validTarget()
	raw, _ := json.Marshal(target)
	for _, bad := range [][]byte{
		[]byte(strings.Replace(string(raw), "destination", "Destination", 1)),
		[]byte(strings.Replace(string(raw), `"name":"fixture"`, `"name":"fixture","proxy_command":"shell"`, 1)),
		[]byte(strings.Replace(string(raw), `"name":"fixture"`, `"name":"fixture","name":"other"`, 1)),
		append(raw, raw...),
		[]byte(strings.Replace(string(raw), `"name":"fixture"`, `"name":null`, 1)),
	} {
		if _, err := DecodeTarget(bad); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}

type captureRunner struct {
	command localexec.Command
	exit    error
	output  localexec.Output
}

func (f *captureRunner) RunInput(_ context.Context, c localexec.Command) (localexec.Output, error) {
	f.command = c
	return f.output, f.exit
}
func pingBytes(t *testing.T) []byte {
	t.Helper()
	b, e := json.Marshal(result.Success("brine host ping", dispatch.PingData{ServerVersion: "test", ProtocolVersions: []int{1}}))
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func TestSSHArgumentsGolden(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	f := &captureRunner{output: localexec.Output{Stdout: pingBytes(t)}}
	client := Client{Runner: f, LookPath: func(string) (string, error) { return "/fixture/ssh", nil }, KnownHostsDir: filepath.Join(t.TempDir(), "pins")}
	request := dispatch.Request{SchemaVersion: 1, Op: "ping", RequestID: "fixture", Args: json.RawMessage(`{}`)}
	if _, err := client.Call(context.Background(), validTarget(), request); err != nil {
		t.Fatal(err)
	}
	known := filepath.Join(client.KnownHostsDir, "fixture.known_hosts")
	want := []string{"-T", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile=" + known, "-o", "GlobalKnownHostsFile=none", "-o", "HostKeyAlgorithms=ssh-ed25519", "-o", "ForwardAgent=no", "-o", "ClearAllForwardings=yes", "-o", "IdentitiesOnly=yes", "-o", "RequestTTY=no", "-o", "PermitLocalCommand=no", "-o", "PreferredAuthentications=publickey", "-o", "VerifyHostKeyDNS=no", "-o", "UpdateHostKeys=no", "-o", "CheckHostIP=no", "-o", "HostKeyAlias=brine-pin", "-o", "ConnectTimeout=5", "-o", "ServerAliveInterval=5", "-o", "ServerAliveCountMax=1", "-i", "/fixture/identity", "--", "runner@fixture.invalid", "brine host serve"}
	if !reflect.DeepEqual(f.command.Args, want) {
		t.Fatalf("argv=%q\nwant=%q", f.command.Args, want)
	}
	if len(f.command.Env) != 0 {
		t.Fatal("client supplied ambient environment")
	}
	decoded, err := dispatch.DecodeRequest(f.command.Stdin)
	if err != nil || decoded.Op != "ping" {
		t.Fatal("request not stdin")
	}
	b, e := os.ReadFile(known)
	if e != nil || string(b) != "brine-pin "+fakeKey()+"\n" {
		t.Fatalf("pin %q %v", b, e)
	}
	info, _ := os.Stat(known)
	if info.Mode().Perm() != 0600 {
		t.Fatal("pin permissions")
	}
	// Never overwrite a changed pin, even when the caller changes its target config.
	if err := os.WriteFile(known, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Call(context.Background(), validTarget(), request); result.ExitCode(err) != 2 {
		t.Fatal("pin drift accepted")
	}
}

func TestFakeSSHExecutable(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "ssh")
	response := string(pingBytes(t))
	script := "#!/bin/sh\n/bin/cat >/dev/null\nprintf '%s\\n' '" + response + "'\nprintf 'synthetic-private-diagnostic' >&2\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	client := Client{KnownHostsDir: filepath.Join(dir, "pins"), LookPath: func(string) (string, error) { return binary, nil }}
	request := dispatch.Request{SchemaVersion: 1, Op: "ping", RequestID: "fixture", Args: json.RawMessage(`{}`)}
	responseValue, err := client.Call(context.Background(), validTarget(), request)
	if err != nil || !responseValue.OK {
		t.Fatalf("%+v %v", responseValue, err)
	}
	for _, script := range []string{
		"#!/bin/sh\nprintf 'synthetic-private-diagnostic' >&2\nexit 255\n",
		"#!/bin/sh\nprintf '{}{}'\n",
		"#!/bin/sh\n/bin/sleep 10\n",
	} {
		if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		_, err := client.Call(ctx, validTarget(), request)
		cancel()
		if err == nil || strings.Contains(err.Error(), "synthetic-private") {
			t.Fatalf("unsafe transport error %v", err)
		}
	}
}

func TestFakeSSHRefusalsAndExitAgreement(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "ssh")
	t.Setenv("PATH", dir)
	client := Client{KnownHostsDir: filepath.Join(dir, "pins"), LookPath: func(string) (string, error) { return binary, nil }}
	request := dispatch.Request{SchemaVersion: 1, Op: "ping", RequestID: "test", Args: json.RawMessage(`{}`)}
	failure, _ := json.Marshal(result.Failure("brine host serve", result.New(result.DispatchRootRefused, nil)))
	for _, tt := range []struct {
		code int
		body []byte
		want result.Code
	}{
		{4, failure, result.DispatchRootRefused},
		{0, failure, result.TransportInvalidResponse},
		{2, failure, result.TransportInvalidResponse},
		{4, pingBytes(t), result.TransportInvalidResponse},
		{255, pingBytes(t), result.TransportFailure},
	} {
		script := "#!/bin/sh\n/bin/cat >/dev/null\nprintf '%s\\n' '" + string(tt.body) + "'\nexit " + strconv.Itoa(tt.code) + "\n"
		if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
		_, err := client.Call(context.Background(), validTarget(), request)
		if err == nil || result.Classify(err).Code() != tt.want {
			t.Fatalf("exit %d body=%s got %v", tt.code, tt.body, err)
		}
	}
}

func TestTargetPinAndKeyRefusals(t *testing.T) {
	for _, bad := range []string{"ssh-ed25519 not-base64", fakeKey() + " comment", strings.Replace(fakeKey(), "ssh-ed25519", "ssh-rsa", 1), "ssh-ed25519 " + base64.StdEncoding.EncodeToString([]byte("not an SSH key"))} {
		target := validTarget()
		target.PinnedHostKey = bad
		if target.Validate() == nil {
			t.Fatalf("accepted key %q", bad)
		}
	}
	for _, mode := range []string{"symlink", "unsafe permissions", "oversized pin"} {
		t.Run(mode, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "pins")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "fixture.known_hosts")
			switch mode {
			case "symlink":
				if err := os.Symlink("missing", path); err != nil {
					t.Fatal(err)
				}
			case "unsafe permissions":
				if err := os.WriteFile(path, []byte("pin"), 0644); err != nil {
					t.Fatal(err)
				}
			default:
				if err := os.WriteFile(path, []byte(strings.Repeat("x", TargetLimit)), 0600); err != nil {
					t.Fatal(err)
				}
			}
			f := &captureRunner{output: localexec.Output{Stdout: pingBytes(t)}}
			client := Client{Runner: f, KnownHostsDir: dir}
			_, err := client.Call(context.Background(), validTarget(), dispatch.Request{SchemaVersion: 1, Op: "ping", RequestID: "test", Args: json.RawMessage(`{}`)})
			if result.ExitCode(err) != 2 || f.command.Path != "" {
				t.Fatal("pin refusal failed before execution")
			}
		})
	}
}

func TestLoadTargetBounds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "target.json")
	raw, err := json.Marshal(validTarget())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadTarget(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, []byte(strings.Repeat(" ", TargetLimit))...), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadTarget(path); result.ExitCode(err) != 2 {
		t.Fatal("target size not bounded")
	}
}

func TestSSHDefaultIgnoresAmbientExecutableAndEnvironment(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte("#!/bin/sh\nexit 99\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("LD_PRELOAD", "/ambient-poison")
	t.Setenv("CONTAINER_HOST", "/ambient-poison")
	t.Setenv("SSH_AUTH_SOCK", "/fixture/agent.sock")
	f := &captureRunner{output: localexec.Output{Stdout: pingBytes(t)}}
	c := Client{Runner: f, KnownHostsDir: filepath.Join(dir, "pins")}
	_, err := c.Call(context.Background(), validTarget(), dispatch.Request{SchemaVersion: 1, Op: "ping", RequestID: "fixture", Args: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if f.command.Path != "/usr/bin/ssh" && f.command.Path != "/bin/ssh" {
		t.Fatal("ambient SSH executable selected")
	}
	if !reflect.DeepEqual(f.command.Env, []string{"SSH_AUTH_SOCK=/fixture/agent.sock"}) {
		t.Fatal("unexpected environment forwarded")
	}
}
