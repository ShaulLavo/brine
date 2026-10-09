// Package quadlet renders and installs Brine-owned rootless container units.
// Callers must verify the recorded plan and normalized desired input during
// apply. Plans deliberately omit literal environment values (P03-02).
package quadlet

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
	"github.com/pelletier/go-toml/v2"
)

const marker = "# Brine-owned plan="

// Unit can only be constructed by Render. Bytes returns a defensive copy.
// Literal environment settings may be sensitive; do not log the contents.
type Unit struct {
	name    string
	content string
}

func (u Unit) Name() string  { return u.name }
func (u Unit) Bytes() []byte { return []byte(u.content) }
func (u Unit) Hash() string  { return digest([]byte(u.content)) }

var hashPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// Render binds the verified normalized input to the plan's DesiredHash and
// index digest. The separately verified platform manifest is the executable
// Image pin; the index and selected platform remain recorded in the artifact.
// This is not plan authentication or permission to deploy an offline preview.
func Render(d policy.Desired, p plan.Plan, platformManifestDigest string) (Unit, error) {
	if err := validateDesired(d); err != nil {
		return Unit{}, err
	}
	raw, err := d.CanonicalBytes()
	if err != nil {
		return Unit{}, fmt.Errorf("quadlet: invalid desired input")
	}
	if digest(raw) != p.DesiredHash || p.App != string(d.Name) || !hashPattern.MatchString(p.Hash) || (p.Kind != plan.Create && p.Kind != plan.Update && p.Kind != plan.NoOp) || len(p.Conflicts) != 0 {
		return Unit{}, fmt.Errorf("quadlet: desired input does not match a non-conflicting plan")
	}
	if !hashPattern.MatchString(p.Image.Digest) || !hashPattern.MatchString(platformManifestDigest) || !strings.HasSuffix(string(d.Image), "@"+p.Image.Digest) || p.Image.Platform.OS != "linux" || (p.Image.Platform.Arch != "amd64" && p.Image.Platform.Arch != "arm64") {
		return Unit{}, fmt.Errorf("quadlet: verified index and platform manifest required")
	}
	if d.AppPorts.Min < 1024 || d.AppPorts.Max < d.AppPorts.Min || p.HostPort < 1024 || p.HostPort > 65535 {
		return Unit{}, fmt.Errorf("quadlet: invalid normalized host port")
	}
	inRange := p.HostPort >= target.Port(d.AppPorts.Min) && p.HostPort <= target.Port(d.AppPorts.Max)
	if p.Kind == plan.Create && !inRange {
		return Unit{}, fmt.Errorf("quadlet: new host port outside normalized policy range")
	}
	for _, change := range p.Changes {
		if change.Kind == plan.AllocatePort && (!inRange || change.Allocation == nil || change.Allocation.Port != p.HostPort || change.Allocation.App != p.App) {
			return Unit{}, fmt.Errorf("quadlet: invalid new host port allocation")
		}
	}
	secrets := slices.Clone(p.Secrets)
	slices.SortFunc(secrets, func(a, b plan.SecretBinding) int { return strings.Compare(a.Environment, b.Environment) })
	wanted := slices.Clone(d.Secrets)
	slices.SortFunc(wanted, func(a, b policy.Secret) int { return strings.Compare(a.Name, b.Name) })
	if len(secrets) != len(wanted) {
		return Unit{}, fmt.Errorf("quadlet: incomplete secret bindings")
	}
	for i, s := range secrets {
		prefix := "brine-" + string(d.Name) + "-" + string(wanted[i].Reference) + "-v"
		version, err := strconv.ParseUint(strings.TrimPrefix(s.VersionName, prefix), 10, 64)
		if s.Environment != wanted[i].Name || s.Reference != wanted[i].Reference || s.ID == "" || err != nil || version == 0 || s.VersionName != prefix+strconv.FormatUint(version, 10) || len(s.VersionName) > 253 {
			return Unit{}, fmt.Errorf("quadlet: invalid immutable secret binding")
		}
	}
	env := slices.Clone(d.Environment)
	slices.SortFunc(env, func(a, b policy.Environment) int { return strings.Compare(a.Name, b.Name) })
	var b strings.Builder
	fmt.Fprintf(&b, "%s%s\n# IndexDigest=%s\n# PlatformManifestDigest=%s\n# Platform=linux/%s\n\n[Unit]\nDescription=Brine app %s\n\n[Container]\n", marker, p.Hash, p.Image.Digest, platformManifestDigest, p.Image.Platform.Arch, d.Name)
	repository, _, _ := strings.Cut(string(d.Image), "@")
	fmt.Fprintf(&b, "Image=%s@%s\nPublishPort=127.0.0.1:%d:%d\nPidsLimit=%d\nPodmanArgs=--memory=%dm\n", repository, platformManifestDigest, p.HostPort, d.ContainerPort, d.Resources.PIDsLimit, d.Resources.MemoryMB)
	for _, s := range secrets {
		fmt.Fprintf(&b, "Secret=%s,type=env,target=%s\n", s.VersionName, s.Environment)
	}
	for _, e := range env {
		fmt.Fprintf(&b, "Environment=%s\n", quoteAssignment(e.Name+"="+e.Value))
	}
	b.WriteString("\n[Service]\nRestart=on-failure\n\n[Install]\nWantedBy=default.target\n")
	return Unit{name: string(d.Name) + ".container", content: b.String()}, nil
}

func digest(b []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(b)) }

func validateDesired(d policy.Desired) error {
	env := map[string]string{}
	for _, e := range d.Environment {
		if _, ok := env[e.Name]; ok {
			return fmt.Errorf("quadlet: duplicate environment key")
		}
		if !utf8.ValidString(e.Value) {
			return fmt.Errorf("quadlet: invalid environment encoding")
		}
		env[e.Name] = e.Value
	}
	secrets := map[string]string{}
	for _, s := range d.Secrets {
		if _, ok := secrets[s.Name]; ok {
			return fmt.Errorf("quadlet: duplicate secret key")
		}
		secrets[s.Name] = string(s.Reference)
	}
	domains := make([]string, len(d.Domains))
	for i, s := range d.Domains {
		domains[i] = string(s)
	}
	raw, err := toml.Marshal(map[string]any{
		"schema_version": d.SchemaVersion,
		"name":           string(d.Name),
		"image":          string(d.Image),
		"container_port": int(d.ContainerPort),
		"domains":        domains,
		"environment":    env,
		"secrets":        secrets,
		"resources":      map[string]int{"memory_mb": d.Resources.MemoryMB, "pids_limit": d.Resources.PIDsLimit},
		"health": map[string]any{
			"path":                     string(d.Health.Path),
			"expected_status":          d.Health.ExpectedStatus,
			"startup_deadline_seconds": d.Health.StartupDeadlineSeconds,
			"timeout_seconds":          d.Health.TimeoutSeconds,
		},
	})
	if err != nil {
		return fmt.Errorf("quadlet: invalid normalized desired input")
	}
	app, err := spec.Parse(raw)
	if err != nil || app.Name != d.Name || app.Image != d.Image || d.PolicyVersion == "" || !hashPattern.MatchString(d.PolicyHash) {
		return fmt.Errorf("quadlet: invalid normalized desired input")
	}
	return nil
}

// Quadlet C-unquotes this word and copies it into generated ExecStart. Neither
// Quadlet nor its serializer escapes systemd expansions, so double both '%' and
// '$' here. Backslashes are C-escaped exactly once for each parser stage.
func quoteAssignment(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '%':
			b.WriteString("%%")
		case '$':
			b.WriteString("$$")
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
