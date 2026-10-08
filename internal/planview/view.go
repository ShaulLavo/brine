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
	"github.com/ShaulLavo/brine/internal/target"
	"github.com/ShaulLavo/brine/internal/ui"
	"github.com/charmbracelet/x/ansi"
)

type change struct {
	Kind               plan.ChangeKind `json:"kind"`
	HostPort           target.Port     `json:"host_port,omitempty"`
	Image              *target.Image   `json:"image,omitempty"`
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
	Kind               plan.Kind                  `json:"kind"`
	App                string                     `json:"app"`
	Target             target.Identity            `json:"target"`
	ObservedGeneration target.Observation[uint64] `json:"observed_generation"`
	PolicyVersion      string                     `json:"policy_version"`
	PolicyHash         string                     `json:"policy_hash"`
	DesiredHash        string                     `json:"desired_hash"`
	ConfigHash         string                     `json:"config_hash"`
	Image              target.Image               `json:"image"`
	HostPort           target.Port                `json:"host_port"`
	Secrets            []secret                   `json:"secrets"`
	Changes            []change                   `json:"changes"`
	Conflicts          []plan.Diagnostic          `json:"conflicts"`
	Hash               string                     `json:"hash"`
}

func project(p plan.Plan) presentation {
	v := presentation{
		SchemaVersion: p.SchemaVersion, Kind: p.Kind, App: p.App,
		Target: p.Target, ObservedGeneration: p.ObservedGeneration,
		PolicyVersion: p.PolicyVersion, PolicyHash: p.PolicyHash,
		DesiredHash: p.DesiredHash, ConfigHash: p.ConfigHash,
		Image: p.Image, HostPort: p.HostPort,
		Secrets: []secret{}, Changes: []change{},
		Conflicts: slices.Clone(p.Conflicts), Hash: p.Hash,
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
				for _, e := range c.Quadlet.Desired.Environment {
					out.EnvironmentKeys = append(out.EnvironmentKeys, e.Name)
				}
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
	add := func(text string) { lines = append(lines, ansi.Hardwrap(safe(text), width, true)) }
	if v.Kind == plan.NoOp {
		add("  No changes required.")
	}
	if v.Kind == plan.Update {
		add("  Previous app values are not recorded in this plan.")
		add("  Listed settings are desired, not a field-level diff.")
	}
	marker := "+"
	if v.Kind == plan.Update {
		marker = "~"
	}
	for _, c := range v.Changes {
		switch c.Kind {
		case plan.AllocatePort:
			add(fmt.Sprintf("%s host port: %d", marker, c.HostPort))
		case plan.PullImage:
			if c.Image != nil {
				add(marker + " image: " + shortDigest(c.Image.Digest, v) + " (" + c.Image.Platform.OS + "/" + c.Image.Platform.Arch + ")")
			} else {
				add(marker + " image: unavailable")
			}
		case plan.BindSecret:
			add(marker + " secret: " + c.Environment + " -> " + c.Reference)
		case plan.RenderQuadlet:
			add(fmt.Sprintf("%s container port: %d", marker, c.ContainerPort))
			for _, key := range c.EnvironmentKeys {
				add(marker + " env " + key + ": changed (value hidden)")
			}
			add(marker + " render app service")
		case plan.StageCaddy:
			for _, domain := range c.Domains {
				add(marker + " domain: " + domain)
			}
			add(fmt.Sprintf("~ routing generation: %d -> %d", c.PreviousGeneration, c.NextGeneration))
		case plan.RestartApp:
			add(marker + " restart app")
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
	others := []string{v.Image.Digest}
	for _, c := range v.Changes {
		if c.Image != nil {
			others = append(others, c.Image.Digest)
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
