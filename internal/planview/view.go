// Package planview presents deployment intent without exposing environment values
// or immutable secret IDs. It does not serialize an executable plan file.
package planview

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
	"github.com/ShaulLavo/brine/internal/ui"
	"github.com/charmbracelet/x/ansi"
)

type change struct {
	Kind               plan.ChangeKind `json:"kind"`
	HostPort           target.Port     `json:"host_port,omitempty"`
	Image              *plan.Image     `json:"image,omitempty"`
	Environment        string          `json:"environment,omitempty"`
	Reference          string          `json:"reference,omitempty"`
	ContainerPort      uint16          `json:"container_port,omitempty"`
	EnvironmentKeys    []string        `json:"environment_keys,omitempty"`
	Domains            []string        `json:"domains,omitempty"`
	PreviousGeneration uint64          `json:"previous_generation,omitempty"`
	NextGeneration     uint64          `json:"next_generation,omitempty"`
}

type secret struct {
	Environment string `json:"environment"`
	Reference   string `json:"reference"`
}

type presentation struct {
	SchemaVersion      int                        `json:"schema_version"`
	Lifecycle          plan.ChangeKind            `json:"lifecycle,omitempty"`
	Kind               plan.Kind                  `json:"kind"`
	App                string                     `json:"app"`
	Target             target.Identity            `json:"target"`
	ObservedGeneration target.Observation[uint64] `json:"observed_generation"`
	PolicyVersion      string                     `json:"policy_version"`
	PolicyHash         string                     `json:"policy_hash"`
	DesiredHash        string                     `json:"desired_hash"`
	ConfigHash         string                     `json:"config_hash"`
	Image              plan.Image                 `json:"image"`
	HostPort           target.Port                `json:"host_port"`
	Secrets            []secret                   `json:"secrets"`
	Changes            []change                   `json:"changes"`
	Diff               *plan.ConfigurationDiff    `json:"diff,omitempty"`
	Conflicts          []plan.Diagnostic          `json:"conflicts"`
	Hash               string                     `json:"hash"`
}

func project(p plan.Plan) presentation {
	v := presentation{
		SchemaVersion: p.SchemaVersion, Kind: p.Kind, Lifecycle: p.Lifecycle, App: p.App,
		Target: p.Target, ObservedGeneration: p.ObservedGeneration,
		PolicyVersion: p.PolicyVersion, PolicyHash: p.PolicyHash,
		DesiredHash: p.DesiredHash, ConfigHash: p.ConfigHash,
		Image: p.Image, HostPort: p.HostPort,
		Secrets: []secret{}, Changes: []change{},
		Diff: canonicalDiff(p.Diff), Conflicts: slices.Clone(p.Conflicts), Hash: p.Hash,
	}
	if v.Conflicts == nil {
		v.Conflicts = []plan.Diagnostic{}
	}
	for _, s := range p.Secrets {
		v.Secrets = append(v.Secrets, secret{s.Environment, string(s.Reference)})
	}
	slices.SortFunc(v.Secrets, func(a, b secret) int {
		if n := strings.Compare(a.Environment, b.Environment); n != 0 {
			return n
		}
		return strings.Compare(a.Reference, b.Reference)
	})
	slices.SortFunc(v.Conflicts, func(a, b plan.Diagnostic) int {
		if n := strings.Compare(string(a.Code), string(b.Code)); n != 0 {
			return n
		}
		return strings.Compare(a.Field, b.Field)
	})
	for _, c := range p.Changes {
		out := change{Kind: c.Kind}
		switch c.Kind {
		case plan.AllocatePort:
			if c.Allocation != nil {
				out.HostPort = c.Allocation.Port
			}
		case plan.PullImage:
			out.Image = c.Image
		case plan.BindSecret:
			if c.Secret != nil {
				out.Environment = c.Secret.Environment
				out.Reference = string(c.Secret.Reference)
			}
		case plan.RenderQuadlet:
			if c.Quadlet != nil {
				out.ContainerPort = uint16(c.Quadlet.Desired.ContainerPort)
				out.EnvironmentKeys = slices.Clone(c.Quadlet.EnvironmentKeys)
				slices.Sort(out.EnvironmentKeys)
			}
		case plan.StageCaddy:
			if c.Caddy != nil {
				out.PreviousGeneration = c.Caddy.Previous
				out.NextGeneration = c.Caddy.Next
				out.HostPort = c.Caddy.HostPort
				for _, d := range c.Caddy.Domains {
					out.Domains = append(out.Domains, string(d))
				}
				slices.Sort(out.Domains)
			}
		}
		v.Changes = append(v.Changes, out)
	}
	return v
}

// JSON returns the compact, redacted CLI data payload, without an envelope or
// trailing newline. Change order is the planner's execution order.
func JSON(p plan.Plan) ([]byte, error) { return json.Marshal(project(p)) }

// Human uses the caller's terminal-aware theme. Non-terminal callers should use
// ui.NewTheme(true). Width defaults to 80 and never expands beyond 80 columns.
func Human(p plan.Plan, theme ui.Theme, width int) string {
	if width <= 0 {
		width = 80
	}
	width = min(width, 80)
	v := project(p)
	lines := []string{theme.Title.Render(ansi.Hardwrap(safe(string(v.Kind)+"  "+v.App), width, true))}
	add := func(text string) {
		lines = append(lines, ansi.Hardwrap(ansi.Wrap(safe(text), width, ""), width, true))
	}
	if v.Kind == plan.NoOp {
		add("  No changes required.")
	}
	manifest := "unknown"
	if v.Image.ManifestDigest.Status == target.KnownStatus && v.Image.ManifestDigest.Value != nil {
		manifest = shortDigest(*v.Image.ManifestDigest.Value, v)
	}
	add("  Platform manifest: " + manifest)
	renderDiff(v, add)
	for _, c := range v.Changes {
		switch c.Kind {
		case plan.AllocatePort:
			add(fmt.Sprintf("+ allocate host port %d", c.HostPort))
		case plan.PullImage:
			if c.Image != nil {
				add("~ pull and verify image " + shortDigest(c.Image.Digest, v))
			} else {
				add("? image unavailable")
			}
		case plan.BindSecret:
			add("~ bind secret " + c.Environment + " -> " + c.Reference)
		case plan.RenderQuadlet:
			add("~ render app service")
		case plan.StageCaddy:
			add(fmt.Sprintf("~ routing generation: %d -> %d", c.PreviousGeneration, c.NextGeneration))
		case plan.RestartApp:
			add("~ restart app")
		case plan.StopApp:
			add("~ stop app (data preserved)")
		case plan.StartApp:
			add("~ start app")
		default:
			add("? unsupported plan change")
		}
	}
	for _, c := range v.Conflicts {
		add("! " + reason(c.Code) + " [" + c.Field + "]")
	}
	return strings.Join(lines, "\n") + "\n"
}

func shortDigest(digest string, v presentation) string {
	if !strings.HasPrefix(digest, "sha256:") || len(digest) != 71 {
		return digest
	}
	others := []string{}
	imageDigests := func(i plan.Image) {
		others = append(others, i.Digest)
		if i.ManifestDigest.Status == target.KnownStatus && i.ManifestDigest.Value != nil {
			others = append(others, *i.ManifestDigest.Value)
		}
	}
	imageDigests(v.Image)
	if v.Diff != nil && v.Diff.Image != nil {
		if v.Diff.Image.From != nil {
			imageDigests(*v.Diff.Image.From)
		}
		if v.Diff.Image.To != nil {
			imageDigests(*v.Diff.Image.To)
		}
	}
	for _, c := range v.Changes {
		if c.Image != nil {
			imageDigests(*c.Image)
		}
	}
	for n := 12; n < 64; n++ {
		prefix := digest[:7+n]
		unique := true
		for _, other := range others {
			if other != digest && strings.HasPrefix(other, prefix) {
				unique = false
				break
			}
		}
		if unique {
			return prefix + "..."
		}
	}
	return digest
}

func safe(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, text)
}

func reason(code plan.ConflictCode) string {
	switch code {
	case plan.UnsupportedTarget:
		return "Target does not support a required capability."
	case plan.UnknownFacts:
		return "Required host facts have not been measured."
	case plan.DomainOwned:
		return "A domain is already served by another app or route."
	case plan.PortOwned:
		return "The host port is already owned by another service."
	case plan.PortsExhausted:
		return "No free port remains in the allowed range."
	case plan.ImagePlatform:
		return "The image does not match the target platform."
	case plan.SecretMissing:
		return "A required secret reference has no installed version."
	case plan.ArtifactDrift:
		return "Live app files differ from the committed release."
	case plan.RuntimeUnavailable:
		return "A required runtime or runner is not ready."
	case plan.StaleState:
		return "Committed state no longer matches the observed target."
	default:
		return "The plan has an unrecognized conflict."
	}
}

func renderDiff(v presentation, add func(string)) {
	d := v.Diff
	if d == nil {
		return
	}
	image := func(i plan.Image) string {
		manifest := "unknown"
		if i.ManifestDigest.Status == target.KnownStatus && i.ManifestDigest.Value != nil {
			manifest = shortDigest(*i.ManifestDigest.Value, v)
		}
		return shortDigest(i.Digest, v) + " (" + i.Platform.OS + "/" + i.Platform.Arch + ", manifest " + manifest + ")"
	}
	renderValue("image", d.Image, image, add)
	if d.Domains != nil {
		for _, domain := range d.Domains.Removed {
			add("- domain: " + string(domain))
		}
		for _, domain := range d.Domains.Added {
			add("+ domain: " + string(domain))
		}
	}
	renderValue("host port", d.HostPort, func(p target.Port) string { return fmt.Sprint(p) }, add)
	renderValue("container port", d.ContainerPort, func(p spec.Port) string { return fmt.Sprint(p) }, add)
	renderValue("resources", d.Resources, func(r policy.Resources) string { return fmt.Sprintf("%d MiB, %d PIDs", r.MemoryMB, r.PIDsLimit) }, add)
	renderValue("health", d.Health, func(h policy.Health) string {
		return fmt.Sprintf("%s status %d, startup %ds, timeout %ds", h.Path, h.ExpectedStatus, h.StartupDeadlineSeconds, h.TimeoutSeconds)
	}, add)
	if d.Environment != nil {
		for _, key := range d.Environment.Removed {
			add("- env " + key + ": removed")
		}
		for _, key := range d.Environment.Added {
			add("+ env " + key + ": added (value hidden)")
		}
		for _, key := range d.Environment.Changed {
			add("~ env " + key + ": changed (value hidden)")
		}
	}
	for _, s := range d.Secrets {
		renderValue("secret "+s.Environment, &plan.ValueChange[plan.SecretVersion]{From: s.From, To: s.To}, func(s plan.SecretVersion) string { return string(s.Reference) + " [" + s.VersionName + "]" }, add)
	}
}

func renderValue[T any](name string, c *plan.ValueChange[T], format func(T) string, add func(string)) {
	if c == nil {
		return
	}
	switch {
	case c.From != nil && c.To != nil:
		add("~ " + name + ": " + format(*c.From) + " -> " + format(*c.To))
	case c.To != nil:
		add("+ " + name + ": " + format(*c.To))
	case c.From != nil:
		add("- " + name + ": " + format(*c.From))
	}
}

func canonicalDiff(input *plan.ConfigurationDiff) *plan.ConfigurationDiff {
	if input == nil {
		return nil
	}
	d := *input
	if input.Domains != nil {
		v := *input.Domains
		v.Added = slices.Clone(v.Added)
		v.Removed = slices.Clone(v.Removed)
		slices.Sort(v.Added)
		slices.Sort(v.Removed)
		d.Domains = &v
	}
	if input.Environment != nil {
		v := *input.Environment
		v.Added = slices.Clone(v.Added)
		v.Removed = slices.Clone(v.Removed)
		v.Changed = slices.Clone(v.Changed)
		slices.Sort(v.Added)
		slices.Sort(v.Removed)
		slices.Sort(v.Changed)
		d.Environment = &v
	}
	d.Secrets = slices.Clone(input.Secrets)
	if d.Secrets == nil {
		d.Secrets = []plan.SecretChange{}
	}
	slices.SortFunc(d.Secrets, func(a, b plan.SecretChange) int { return strings.Compare(a.Environment, b.Environment) })
	return &d
}
