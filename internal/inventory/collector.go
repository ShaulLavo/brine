// Package inventory collects read-only host facts in the target snapshot contract.
package inventory

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/target"
)

const fileLimit = 1 << 20

// FileSystem implementations must honor the collection context. HostFS also
// limits each operation to three seconds and caps outstanding kernel calls.
type FileSystem interface {
	ReadFile(context.Context, string) ([]byte, error)
	ReadDir(context.Context, string) ([]fs.DirEntry, error)
	Readlink(context.Context, string) (string, error)
}

type HostFS struct{}

// IdentityKey is operator-provided, stable across collections, and never emitted.
// Changing it changes the target ID. No fallback to the raw machine ID exists.
// StateGeneration connects the future control database without assuming its schema.
// A present state directory without a state reader produces unknown, not zero.
type Collector struct {
	FS              FileSystem
	Runner          localexec.StdoutRunner
	IdentityKey     []byte
	RunnerUser      string
	StateGeneration func(context.Context) (uint64, error)
}

func unknown[T any]() target.Observation[T] { return target.Observation[T]{Status: target.Unknown} }
func absent[T any]() target.Observation[T]  { return target.Observation[T]{Status: target.Absent} }
func digest(b []byte) string                { h := sha256.Sum256(b); return "sha256:" + hex.EncodeToString(h[:]) }

func (c Collector) probe(ctx context.Context, p string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	s, e := c.Runner.RunStdout(ctx, p, args...)
	if len(s) >= localexec.OutputLimit {
		return "", fmt.Errorf("probe output limit reached")
	}
	return strings.TrimSpace(s), e
}

func (c Collector) Collect(ctx context.Context) (target.Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if c.FS == nil || c.Runner == nil || len(c.IdentityKey) == 0 {
		return target.Snapshot{}, fmt.Errorf("inventory requires filesystem, runner and identity key")
	}
	if c.RunnerUser == "" {
		c.RunnerUser = "brine"
	}
	if !regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`).MatchString(c.RunnerUser) {
		return target.Snapshot{}, fmt.Errorf("invalid inventory runner user")
	}
	s := target.Snapshot{SchemaVersion: target.SchemaVersion, CgroupV2: unknown[bool](), Runner: target.Runner{User: unknown[string](), Linger: unknown[bool]()}, Generation: unknown[uint64](), CaddyConfig: unknown[target.CaddyConfigSet](), Apps: unknown[[]target.App](), UsedPorts: unknown[[]target.Port](), PortOwners: unknown[[]target.PortOwner](), LiveCaddyFiles: unknown[[]target.LiveCaddyFile](), FreeDiskBytes: unknown[uint64]()}
	data, e := c.FS.ReadFile(ctx, "/etc/os-release")
	if e != nil {
		return s, fmt.Errorf("cannot read target OS")
	}
	values, e := osRelease(string(data))
	if e != nil {
		return s, e
	}
	s.OS = target.OS{ID: values["ID"], Version: values["VERSION_ID"]}
	arch, e := c.probe(ctx, "uname", "-m")
	if e != nil {
		return s, fmt.Errorf("cannot read target architecture")
	}
	switch arch {
	case "aarch64":
		arch = "arm64"
	case "x86_64":
		arch = "amd64"
	}
	s.Arch = arch
	machine, e := c.FS.ReadFile(ctx, "/etc/machine-id")
	if e != nil {
		return s, fmt.Errorf("cannot read target identity")
	}
	id := strings.TrimSpace(string(machine))
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(id) || id == strings.Repeat("0", 32) {
		return s, fmt.Errorf("invalid target identity")
	}
	h := hmac.New(sha256.New, c.IdentityKey)
	h.Write([]byte("brine-target-v1\x00" + id))
	s.Identity.ID = "host-" + hex.EncodeToString(h.Sum(nil))
	key, e := c.FS.ReadFile(ctx, "/etc/ssh/ssh_host_ed25519_key.pub")
	if e != nil {
		return s, fmt.Errorf("cannot read SSH host public key")
	}
	fields := strings.Fields(string(key))
	if len(fields) < 2 || fields[0] != "ssh-ed25519" {
		return s, fmt.Errorf("invalid SSH host public key")
	}
	blob, e := base64.StdEncoding.DecodeString(fields[1])
	if e != nil || len(blob) != 51 || string(blob[4:15]) != "ssh-ed25519" || blob[0] != 0 || blob[1] != 0 || blob[2] != 0 || blob[3] != 11 || blob[15] != 0 || blob[16] != 0 || blob[17] != 0 || blob[18] != 32 {
		return s, fmt.Errorf("invalid SSH host public key")
	}
	hash := sha256.Sum256(blob)
	s.Identity.HostKeyFingerprint = "SHA256:" + base64.RawStdEncoding.EncodeToString(hash[:])
	s.Versions = target.Versions{Systemd: c.version(ctx, "systemctl", []string{"--version"}), Podman: c.version(ctx, "podman", []string{"version", "--format", "json"}), Passt: c.version(ctx, "passt", []string{"--version"}), Caddy: c.version(ctx, "caddy", []string{"version"}), Litestream: c.version(ctx, "litestream", []string{"version"})}
	if s.Versions.Passt.Status == target.Unknown {
		if out, err := c.probe(ctx, "dpkg-query", "-W", "-f=${Version}", "passt"); err == nil && versionToken.MatchString(out) && regexp.MustCompile(`^[0-9]`).MatchString(out) {
			s.Versions.Passt = target.Known(out)
		}
	}
	if _, e = c.FS.ReadFile(ctx, "/sys/fs/cgroup/cgroup.controllers"); e == nil {
		s.CgroupV2 = target.Known(true)
	} else if errors.Is(e, fs.ErrNotExist) {
		if _, e = c.FS.ReadDir(ctx, "/sys/fs/cgroup"); e == nil {
			s.CgroupV2 = target.Known(false)
		}
	}
	home, exists := c.runner(ctx, &s)
	c.generation(ctx, &s, home, exists)
	c.disk(ctx, &s, home)
	c.apps(ctx, &s, home, exists)
	c.listeners(ctx, &s, home)
	if e = c.caddy(ctx, &s); e != nil {
		return s, e
	}
	if err := ctx.Err(); err != nil {
		return s, err
	}
	// Encode validates shape while retaining unsupported operating systems.
	if _, e = target.Encode(s); e != nil {
		return s, fmt.Errorf("invalid collected snapshot: %w", e)
	}
	return s, nil
}

func osRelease(data string) (map[string]string, error) {
	m := map[string]string{}
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}

		v, e := releaseValue(v)
		if e != nil {
			return nil, e
		}
		m[k] = v
	}
	if m["ID"] == "" || m["VERSION_ID"] == "" {
		return nil, fmt.Errorf("incomplete OS metadata")
	}
	return m, nil
}

var versionToken = regexp.MustCompile(`^[!-~]{1,128}$`)

func (c Collector) version(ctx context.Context, p string, args []string) target.Observation[string] {
	s, e := c.probe(ctx, p, args...)
	if e != nil {
		if errors.Is(e, fs.ErrNotExist) || errors.Is(e, exec.ErrNotFound) {
			return absent[string]()
		}
		return unknown[string]()
	}
	var v string
	fields := strings.Fields(s)
	switch p {
	case "podman":
		var data struct{ Client struct{ Version string } }
		if json.Unmarshal([]byte(s), &data) == nil {
			v = data.Client.Version
		}
	case "systemctl":
		if len(fields) > 1 && fields[0] == "systemd" {
			v = fields[1]
		}
	case "passt":
		if len(fields) > 1 && strings.TrimSuffix(fields[0], ":") == "passt" {
			v = fields[1]
		}
	case "caddy", "litestream":
		if len(fields) > 0 {
			v = fields[0]
			if v == p && len(fields) > 1 {
				v = fields[1]
			}
		}
	}
	if !versionToken.MatchString(v) || !regexp.MustCompile(`^v?[0-9]`).MatchString(v) {
		return unknown[string]()
	}
	return target.Known(v)
}

func (c Collector) runner(ctx context.Context, s *target.Snapshot) (string, bool) {
	home := "/home"
	data, e := c.FS.ReadFile(ctx, "/etc/passwd")
	if e != nil {
		return home, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Split(line, ":")

		if f[0] == c.RunnerUser && (len(f) != 7 || !filepath.IsAbs(f[5])) {
			return home, false
		}
		if len(f) == 7 && f[0] == c.RunnerUser && filepath.IsAbs(f[5]) {
			home = f[5]
			s.Runner.User = target.Known(c.RunnerUser)
			if _, e = c.FS.ReadFile(ctx, "/var/lib/systemd/linger/"+c.RunnerUser); e == nil {
				s.Runner.Linger = target.Known(true)
			} else if errors.Is(e, fs.ErrNotExist) {
				s.Runner.Linger = target.Known(false)
			}
			return home, true
		}
	}
	s.Runner.User = absent[string]()
	return home, false
}
func (c Collector) generation(ctx context.Context, s *target.Snapshot, home string, exists bool) {
	if c.StateGeneration != nil {
		v, e := c.StateGeneration(ctx)
		if e == nil {
			s.Generation = target.Known(v)
		}
		return
	}
	if s.Runner.User.Status == target.Unknown {
		return
	}
	if !exists {
		s.Generation = target.Known(uint64(0))
		return
	}
	_, e := c.FS.ReadDir(ctx, filepath.Join(home, ".local/state/brine"))
	if errors.Is(e, fs.ErrNotExist) {
		s.Generation = target.Known(uint64(0))
	}
}
func (c Collector) disk(ctx context.Context, s *target.Snapshot, home string) {
	if s.Runner.User.Status == target.Unknown {
		return
	}
	p := filepath.Join(home, ".local/state/brine")
	if home == "/home" {
		p = home
	}
	for {
		_, e := c.FS.ReadDir(ctx, p)
		if e == nil || !errors.Is(e, fs.ErrNotExist) || p == "/" {
			break
		}
		p = filepath.Dir(p)
	}
	out, e := c.probe(ctx, "df", "-B1", "--output=avail", p)
	if e != nil {
		return
	}
	f := strings.Fields(out)
	if len(f) != 2 || f[0] != "Avail" {
		return
	}
	v, e := strconv.ParseUint(f[1], 10, 64)
	if e == nil {
		s.FreeDiskBytes = target.Known(v)
	}
}

func releaseValue(value string) (string, error) {
	quote := byte(0)
	if len(value) > 0 && (value[0] == '\'' || value[0] == '"') {
		quote = value[0]
		if len(value) < 2 || value[len(value)-1] != quote {
			return "", fmt.Errorf("invalid OS metadata")
		}
		value = value[1 : len(value)-1]
	}
	if quote == '\'' {
		if strings.ContainsRune(value, '\'') {
			return "", fmt.Errorf("invalid OS metadata")
		}
		return value, nil
	}
	var out strings.Builder
	for i := 0; i < len(value); i++ {
		ch := value[i]
		if ch == '\\' {
			if i+1 == len(value) {
				return "", fmt.Errorf("invalid OS metadata")
			}
			next := value[i+1]
			if quote == 0 || next == '"' || next == '\\' || next == '$' || next == '`' {
				out.WriteByte(next)
				i++
				continue
			}
			out.WriteByte(ch)
			continue
		}
		if ch == '"' || (quote == 0 && (ch == '\'' || ch == ' ' || ch == '\t')) {
			return "", fmt.Errorf("invalid OS metadata")
		}
		out.WriteByte(ch)
	}
	return out.String(), nil
}
