package transport

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/localexec"
)

func TestSSHConfigSafetyPrecedence(t *testing.T) {
	ssh, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("system OpenSSH unavailable")
	}
	dir := t.TempDir()
	config := filepath.Join(dir, "config")
	marker := filepath.Join(dir, "must-not-exist")
	contents := `Host fixture_alias fixture.invalid
 HostName fixture.invalid
 User runner
 Port 2222
 ProxyJump fixture-jump
 IdentityAgent SSH_AUTH_SOCK
 IdentityFile /fixture/config-identity
 ForwardAgent yes
 ClearAllForwardings no
 LocalForward 20001 fixture.invalid:20002
 RemoteForward 20003 fixture.invalid:20004
 DynamicForward 20005
 PermitLocalCommand yes
 LocalCommand touch ` + marker + `
 BatchMode no
 StrictHostKeyChecking no
 UserKnownHostsFile /fixture/config-pin
 GlobalKnownHostsFile /fixture/global-pin
 IdentitiesOnly no
 RequestTTY force
`
	if err := os.WriteFile(config, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSH_AUTH_SOCK", filepath.Join(dir, "agent.sock"))
	runner := &captureRunner{output: localexec.Output{Stdout: pingBytes(t)}}
	client := Client{Runner: runner, KnownHostsDir: filepath.Join(dir, "pins"), LookPath: func(string) (string, error) { return ssh, nil }}
	target := validTarget()
	target.IdentityPath = filepath.Join(dir, "identity")
	if err := os.WriteFile(target.IdentityPath, []byte("synthetic identity fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	// Use an explicit destination first so the old implementation reaches ssh -G.
	_, err = client.Call(context.Background(), target, dispatch.Request{SchemaVersion: 1, Op: "ping", RequestID: "test", Args: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	args := append([]string{"-G", "-F", config}, runner.command.Args...)
	cmd := exec.Command(ssh, args...)
	cmd.Env = runner.command.Env
	output, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	got := string(output)
	for _, want := range []string{"port 2222\n", "forwardagent no\n", "clearallforwardings yes\n", "permitlocalcommand no\n", "batchmode yes\n", "stricthostkeychecking true\n", "identitiesonly yes\n", "requesttty false\n", "globalknownhostsfile none\n", "userknownhostsfile " + filepath.Join(client.KnownHostsDir, "fixture.known_hosts") + "\n", "identityfile " + target.IdentityPath + "\n", "identityfile /fixture/config-identity\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in config output\n%s", want, got)
		}
	}
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "localforward ") || strings.HasPrefix(line, "remoteforward ") || strings.HasPrefix(line, "dynamicforward ") {
			t.Errorf("forward survived %q", line)
		}
	}
	if !strings.Contains(strings.Join(runner.command.Env, "\n"), "SSH_AUTH_SOCK="+os.Getenv("SSH_AUTH_SOCK")) {
		t.Error("agent environment discarded")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("LocalCommand executed")
	}
	target.Destination = "fixture_alias"
	_, err = client.Call(context.Background(), target, dispatch.Request{SchemaVersion: 1, Op: "ping", RequestID: "test", Args: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("alias refused: %v", err)
	}
	cmd = exec.Command(ssh, append([]string{"-G", "-F", config}, runner.command.Args...)...)
	cmd.Env = runner.command.Env
	output, err = cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"hostname fixture.invalid\n", "user runner\n", "port 2222\n", "proxyjump fixture-jump\n"} {
		if !strings.Contains(string(output), want) {
			t.Errorf("alias config missing %q", want)
		}
	}
}
