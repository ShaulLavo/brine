package enroll

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/localexec"
	operatorpolicy "github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/transport"
)

type permissionsSSH struct {
	command  localexec.Command
	calls    int
	response []byte
	err      error
}

func (s *permissionsSSH) RunInput(_ context.Context, command localexec.Command) (localexec.Output, error) {
	command.Stdin = bytes.Clone(command.Stdin)
	s.command = command
	s.calls++
	return localexec.Output{Stdout: s.response}, s.err
}

func permissionsClientFixture(t *testing.T) (transport.Target, string) {
	t.Helper()
	dir := t.TempDir()
	blob := make([]byte, 51)
	blob[3] = 11
	copy(blob[4:], "ssh-ed25519")
	blob[18] = 32
	target := transport.Target{Name: "fixture", Destination: "brine@fixture", IdentityPath: "/fixture/restricted-deploy-key", PinnedHostKey: "ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob)}
	if err := os.WriteFile(filepath.Join(dir, "fixture.identity-key"), make([]byte, 32), 0600); err != nil {
		t.Fatal(err)
	}
	return target, dir
}
func TestPermissionsOperatorSSHUsesPinnedHostAndFixedHelper(t *testing.T) {
	target, dir := permissionsClientFixture(t)
	raw, err := json.Marshal(result.Success("brine host permissions", PermissionsResponse{PolicyHash: "sha256:" + strings.Repeat("a", 64), Permissions: &operatorpolicy.Permissions{Registries: []operatorpolicy.Registry{}, Domains: []string{}}}))
	if err != nil {
		t.Fatal(err)
	}
	ssh := &permissionsSSH{response: raw}
	if _, err = (PermissionsClient{Runner: ssh}).Call(t.Context(), target, dir, "operator@fixture", PermissionsRequest{Action: "show"}); err != nil {
		t.Fatal(err)
	}
	if ssh.calls != 1 || ssh.command.Path != "ssh" || ssh.command.Args[len(ssh.command.Args)-1] != permissionsHostCommand {
		t.Fatal("not one fixed admin request")
	}
	for _, option := range []string{"StrictHostKeyChecking=yes", "HostKeyAlias=brine-pin", "GlobalKnownHostsFile=none", "BatchMode=yes", "ForwardAgent=no", "PermitLocalCommand=no"} {
		if !slices.Contains(ssh.command.Args, option) {
			t.Fatal("missing option", option)
		}
	}
	if slices.Contains(ssh.command.Args, target.IdentityPath) || slices.Contains(ssh.command.Args, target.Destination) {
		t.Fatal("restricted deploy credential reused for policy administration")
	}
	var request PermissionsRequest
	if err = json.Unmarshal(ssh.command.Stdin, &request); err != nil || request.HostKey != target.PinnedHostKey || len(request.IdentityKey) != 32 {
		t.Fatal("missing enrollment binding", err)
	}
	// #nosec G304 -- Read the exact known-host fixture created inside t.TempDir.
	pin, err := os.ReadFile(filepath.Join(dir, "pins", "fixture.known_hosts"))
	if err != nil || string(pin) != "brine-pin "+target.PinnedHostKey+"\n" {
		t.Fatal("wrong operator host pin", err)
	}
}
func TestPermissionsClientNeverRetriesUnknownOutcome(t *testing.T) {
	target, dir := permissionsClientFixture(t)
	ssh := &permissionsSSH{err: errors.New("lost response")}
	request := PermissionsRequest{Action: "apply", PlanID: strings.Repeat("a", 64), IdempotencyKey: strings.Repeat("b", 32)}
	if _, err := (PermissionsClient{Runner: ssh}).Call(t.Context(), target, dir, "operator@fixture", request); err == nil || ssh.calls != 1 {
		t.Fatal("unknown request replayed", err)
	}
}
func TestPermissionsClientRejectsMalformedOrUnrelatedResponses(t *testing.T) {
	for _, raw := range []string{`null`, `{}`, `{"schema_version":1,"command":"brine host permissions","ok":false,"data":null,"error":null}`, `{"schema_version":1,"command":"brine host permissions","ok":true,"data":{},"error":null}`, `{"schema_version":1,"command":"brine host serve","ok":true,"data":{},"error":null}`} {
		if _, err := decodePermissionsResponse([]byte(raw), PermissionsRequest{Action: "show"}); err == nil {
			t.Fatal("malformed response accepted", raw)
		}
	}
}
