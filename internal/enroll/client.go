package enroll

import (
	"context"
	"crypto/rand"
	"debug/elf"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/transport"
)

type Options struct {
	Destination, Name, DeployKeyPath, IdentityPath, HostBinary, ConfigDir string
	Undo, NoInput                                                         bool
}
type Client struct {
	Runner   localexec.InputRunner
	Input    io.Reader
	Output   io.Writer
	Terminal bool
}

var adminDestination = regexp.MustCompile(`^(?:[a-z_][a-z0-9_-]{0,31}@)?[A-Za-z0-9_][A-Za-z0-9_.-]{0,252}$`)
var targetName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
var tempDirectory = regexp.MustCompile(`^/tmp/brine-enroll\.[A-Za-z0-9]{8}$`)

func (c Client) exec(ctx context.Context, path string, args []string, input []byte, timeout time.Duration) ([]byte, error) {
	runner := c.Runner
	if runner == nil {
		runner = localexec.ExecRunner{}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var env []string
	if agent := os.Getenv("SSH_AUTH_SOCK"); agent != "" {
		env = []string{"SSH_AUTH_SOCK=" + agent}
	}
	out, err := runner.RunInput(ctx, localexec.Command{Path: path, Args: args, Stdin: input, Env: env, OutputLimit: dispatch.ResponseLimit})
	if err != nil {
		if c.Output != nil {
			fmt.Fprint(c.Output, string(out.Stderr))
		}
		return nil, errors.New("operator SSH command failed; inspect and reconcile the host before retrying")
	}
	return out.Stdout, nil
}
func adminArgs(dest string) []string {
	return []string{"-T", "-o", "StrictHostKeyChecking=yes", "-o", "ForwardAgent=no", "-o", "ClearAllForwardings=yes", "-o", "PermitLocalCommand=no", "-o", "ConnectTimeout=10", "--", dest}
}
func DefaultTargetName(destination string) string {
	parts := strings.Split(destination, "@")
	name := strings.ToLower(parts[len(parts)-1])
	name = strings.NewReplacer(".", "-", "_", "-").Replace(name)
	if len(name) == 0 || name[0] < 'a' || name[0] > 'z' {
		name = "host-" + name
	}
	if len(name) > 63 {
		name = name[:63]
	}
	return strings.TrimRight(name, "-")
}

func (c Client) Run(ctx context.Context, o Options) error {
	if !adminDestination.MatchString(o.Destination) || !targetName.MatchString(o.Name) || !filepath.IsAbs(o.ConfigDir) {
		return errors.New("invalid enrollment destination, target name or config directory")
	}
	if c.Output == nil || c.Input == nil {
		return errors.New("enrollment needs operator input and output")
	}
	if !c.Terminal || o.NoInput {
		return errors.New("enrollment requires terminal stdin; --yes cannot authorize it")
	}
	if err := privateDir(o.ConfigDir); err != nil {
		return err
	}
	keyPath := filepath.Join(o.ConfigDir, o.Name+".identity-key")
	identityKey, err := clientKey(keyPath)
	if err != nil {
		return err
	}
	args := adminArgs(o.Destination)
	bootstrap, err := c.exec(ctx, "ssh", append(append([]string{}, args...), "uname -m; id -u"), nil, 30*time.Second)
	if err != nil {
		return err
	}
	facts := strings.Fields(string(bootstrap))
	if len(facts) != 2 {
		return errors.New("invalid architecture probe")
	}
	arch := ""
	switch facts[0] {
	case "aarch64":
		arch = "arm64"
	case "x86_64":
		arch = "amd64"
	default:
		return errors.New("unsupported host architecture")
	}
	localBinary := o.HostBinary
	if localBinary == "" {
		localBinary, err = os.Executable()
		if err != nil {
			return err
		}
	}
	binary, err := elf.Open(localBinary)
	if err != nil {
		return errors.New("a matching Linux ELF binary is required; use --host-binary")
	}
	machine := elf.EM_X86_64
	if arch == "arm64" {
		machine = elf.EM_AARCH64
	}
	matches := binary.Machine == machine
	binary.Close()
	if !matches {
		return errors.New("host binary architecture mismatch; supply --host-binary for the target")
	}
	temp, err := c.exec(ctx, "ssh", append(append([]string{}, args...), "mktemp -d /tmp/brine-enroll.XXXXXXXX"), nil, 30*time.Second)
	if err != nil {
		return err
	}
	dir := strings.TrimSpace(string(temp))
	if !tempDirectory.MatchString(dir) {
		return errors.New("invalid private remote temp directory")
	}
	defer func() {
		cleanCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = c.exec(cleanCtx, "ssh", append(append([]string{}, args...), "rm -- "+dir+"/brine; rmdir -- "+dir), nil, 30*time.Second)
	}()
	scp := []string{"-q", "-o", "StrictHostKeyChecking=yes", "-o", "ForwardAgent=no", "-o", "ClearAllForwardings=yes", "-o", "PermitLocalCommand=no", "--", localBinary, o.Destination + ":" + dir + "/brine"}
	if _, err = c.exec(ctx, "scp", scp, nil, time.Minute); err != nil {
		return err
	}
	prefix := "sudo -n "
	if facts[1] == "0" {
		prefix = ""
	}
	remote := append(append([]string{}, args...), prefix+dir+"/brine host enrollment")
	confirmed := ""
	call := func(action, key string) ([]byte, error) {
		data, err := json.Marshal(HostRequest{Action: action, IdentityKey: identityKey, DeployKey: key, Confirmed: confirmed})
		if err != nil {
			return nil, err
		}
		return c.exec(ctx, "ssh", remote, data, 10*time.Minute)
	}
	probeAction := "probe"
	if o.Undo {
		probeAction = "undo-probe"
	}
	data, err := call(probeAction, "")
	if err != nil {
		return err
	}
	var f Facts
	if err = json.Unmarshal(data, &f); err != nil {
		return errors.New("invalid remote enrollment inventory")
	}
	var deployKey string
	configPath := filepath.Join(o.ConfigDir, o.Name+".json")
	if o.Undo {
		existing, err := transport.LoadTarget(configPath)
		if err != nil && !f.OwnedRunner {
			return errors.New("undo needs the existing pinned target config or an operator-authenticated partial enrollment")
		}
		if err == nil && existing.PinnedHostKey != f.HostKey {
			return errors.New("undo refused: host key changed")
		}
		fmt.Fprintln(c.Output, "Undo removes only recorded enrollment files, the verified Brine-created runner home/account and linger. Existing packages and services keep their original state. Drift aborts cleanup.")
	} else {
		plan, err := MakePlan(f)
		if err != nil {
			return err
		}
		confirmed, err = confirmationBinding(f)
		if err != nil {
			return err
		}
		for i, change := range plan.Changes {
			fmt.Fprintf(c.Output, "%d. %s\n", i+1, change)
		}
		for _, pkg := range f.PackageInstall {
			fmt.Fprintf(c.Output, "Package transaction: install %s=%s\n", pkg.Name, pkg.Version)
		}
		public, err := os.ReadFile(o.DeployKeyPath)
		if err != nil || len(public) > 16<<10 {
			return errors.New("--deploy-key must name a public key file")
		}
		deployKey, err = PublicKey(strings.TrimSpace(string(public)))
		if err != nil {
			return err
		}
		if o.IdentityPath == "" {
			o.IdentityPath = strings.TrimSuffix(o.DeployKeyPath, ".pub")
		}
		o.IdentityPath, err = filepath.Abs(o.IdentityPath)
		if err != nil {
			return err
		}
		info, statErr := os.Lstat(o.IdentityPath)
		if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return errors.New("deploy private key must exist as a private regular file")
		}
		if o.IdentityPath == o.DeployKeyPath {
			return errors.New("public key path needs a .pub suffix or an explicit --identity")
		}
		target := transport.Target{Name: o.Name, Destination: "brine@" + strings.Split(o.Destination, "@")[len(strings.Split(o.Destination, "@"))-1], IdentityPath: o.IdentityPath, PinnedHostKey: f.HostKey}
		if err = target.Validate(); err != nil {
			return err
		}
		if old, err := transport.LoadTarget(configPath); err == nil && old != target {
			return errors.New("target config would overwrite an existing target")
		}
	}
	fmt.Fprintf(c.Output, "Type target name %q to confirm: ", o.Name)
	if err = Confirm(c.Input, c.Terminal, o.NoInput, o.Name); err != nil {
		return err
	}
	if o.Undo {
		raw, undoErr := call("undo", "")
		if undoErr != nil {
			return undoErr
		}
		var undone UndoResult
		if err := json.Unmarshal(raw, &undone); err != nil || !undone.Removed {
			return errors.New("invalid enrollment undo result")
		}
		if undone.RetainedPolicy {
			fmt.Fprintln(c.Output, "Operator-edited policy retained at /etc/ssh/brine/operator-policy.toml.")
		}
		if err = os.Remove(configPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err = os.Remove(keyPath); err != nil {
			return err
		}
		pin := filepath.Join(o.ConfigDir, "pins", o.Name+".known_hosts")
		if err = os.Remove(pin); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		fmt.Fprintln(c.Output, "Enrollment removed and runner absence verified.")
		return nil
	}
	if _, err = call("apply", deployKey); err != nil {
		return err
	}
	target := transport.Target{Name: o.Name, Destination: "brine@" + strings.Split(o.Destination, "@")[len(strings.Split(o.Destination, "@"))-1], IdentityPath: o.IdentityPath, PinnedHostKey: f.HostKey}
	tc := transport.Client{Runner: c.Runner, KnownHostsDir: filepath.Join(o.ConfigDir, "pins")}
	request := dispatch.Request{SchemaVersion: 1, Op: "ping", RequestID: "enrollment-verification", Args: json.RawMessage(`{}`)}
	if _, err = tc.Call(ctx, target, request); err != nil {
		return err
	}
	if _, err = tc.VerifyRestriction(ctx, target, request); err != nil {
		return err
	}
	request.Op = "inventory"
	if _, err = tc.Call(ctx, target, request); err != nil {
		return err
	}
	data, err = call("verify", "")
	if err != nil {
		return err
	}
	var verified Facts
	if err = json.Unmarshal(data, &verified); err != nil {
		return err
	}
	if _, err = MakePlan(verified); err != nil {
		return err
	}
	if len(verified.Packages) != 3 {
		return errors.New("post-enrollment runtime packages are missing")
	}
	if !verified.OwnedRunner || verified.Snapshot.Runner.Linger.Value == nil || !*verified.Snapshot.Runner.Linger.Value {
		return errors.New("post-enrollment inventory did not verify the runner")
	}
	config, err := json.MarshalIndent(target, "", "  ")
	if err != nil {
		return err
	}
	if err = clientFile(configPath, append(config, '\n')); err != nil {
		return err
	}
	fmt.Fprintf(c.Output, "Enrolled and verified. Target config: %s\n", configPath)
	return nil
}
func privateDir(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	s, err := os.Lstat(path)
	if err != nil || !s.IsDir() || s.Mode().Perm() != 0700 {
		return errors.New("client config directory must be a private directory, not a symlink")
	}
	return nil
}
func clientFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		s, err := os.Lstat(path)
		if err != nil || !s.Mode().IsRegular() || s.Mode().Perm() != 0600 {
			return errors.New("client target file is not private")
		}
		old, err := os.ReadFile(path)
		if err != nil || string(old) != string(data) {
			return errors.New("existing client target differs; refusing overwrite")
		}
		return nil
	}
	if err != nil {
		return err
	}
	return writeSync(f, data)
}
func clientKey(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		s, err := os.Lstat(path)
		if err != nil || !s.Mode().IsRegular() || s.Mode().Perm() != 0600 || len(data) != 32 {
			return nil, errors.New("invalid private client identity key")
		}
		return data, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	data = make([]byte, 32)
	if _, err = rand.Read(data); err != nil {
		return nil, err
	}
	return data, clientFile(path, data)
}

type HostRequest struct {
	Confirmed   string `json:"confirmed,omitempty"`
	Action      string `json:"action"`
	IdentityKey []byte `json:"identity_key"`
	DeployKey   string `json:"deploy_key"`
}

func PublicKey(input string) (string, error) {
	fields := strings.Fields(input)
	if len(fields) < 2 || len(fields) > 3 || strings.ContainsAny(input, "\r\n") {
		return "", errors.New("deploy key must be one plain Ed25519 public key")
	}
	key := strings.Join(fields[:2], " ")
	// Target validation shares the canonical Ed25519 wire-format guard.
	if err := (transport.Target{Name: "fixture", Destination: "brine@fixture", IdentityPath: "/fixture/key", PinnedHostKey: key}).Validate(); err != nil {
		return "", errors.New("invalid deploy public key")
	}
	return key, nil
}
func writeSync(f *os.File, data []byte) error {
	_, err := f.Write(data)
	return errors.Join(err, f.Sync(), f.Close())
}
