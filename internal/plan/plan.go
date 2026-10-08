// Package plan computes deterministic deployment intent without host adapters,
// storage, network access or time. Build does not authorize or apply changes.
package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
)

const SchemaVersion = 1

type Kind string

const (
	Create   Kind = "create"
	Update   Kind = "update"
	NoOp     Kind = "no-op"
	Conflict Kind = "conflict"
)

type ConflictCode string

const (
	UnsupportedTarget ConflictCode = "unsupported_target"
	UnknownFacts      ConflictCode = "unknown_facts"
	DomainOwned       ConflictCode = "domain_owned"
	PortOwned         ConflictCode = "port_owned"
	PortsExhausted    ConflictCode = "ports_exhausted"
	ImagePlatform     ConflictCode = "image_platform"
	SecretMissing     ConflictCode = "secret_missing"
	ArtifactDrift     ConflictCode = "artifact_drift"
)

type Diagnostic struct {
	Code  ConflictCode `json:"code"`
	Field string       `json:"field"`
}

type Input struct {
	Desired  policy.Desired
	Snapshot target.Snapshot
	// Image is manifest metadata supplied by the caller, never looked up by Build.
	Image    target.Image
	Evidence Evidence
}

// Evidence fills facts not represented by the v1 inventory schema. The caller
// must obtain it from the same target/generation as Snapshot. An empty app owner
// means an unrelated route or listener, never permission to overwrite it.
type Evidence struct {
	Routes    target.Observation[[]Route]    `json:"routes"`
	Listeners target.Observation[[]Listener] `json:"listeners"`
	Applied   target.Observation[[]Applied]  `json:"applied"`
}
type Route struct {
	Domain spec.Domain `json:"domain"`
	App    string      `json:"app"`
}
type Listener struct {
	Port target.Port `json:"port"`
	App  string      `json:"app"`
}

// Applied records the configuration fingerprint and artifact digests from the
// last committed release. It is not reconstructed from the new desired state.
type Applied struct {
	App        string        `json:"app"`
	ConfigHash string        `json:"config_hash"`
	Units      []target.Unit `json:"units"`
	CaddyHash  string        `json:"caddy_hash"`
}

type SecretBinding struct {
	Environment string               `json:"environment"`
	Reference   spec.SecretReference `json:"reference"`
	VersionName string               `json:"version_name"`
	ID          string               `json:"id"`
}

type ChangeKind string

const (
	AllocatePort  ChangeKind = "allocate_port"
	PullImage     ChangeKind = "pull_image"
	BindSecret    ChangeKind = "bind_secret_version"
	RenderQuadlet ChangeKind = "render_quadlet"
	StageCaddy    ChangeKind = "stage_caddy_generation"
	RestartApp    ChangeKind = "restart_app"
)

// Change is a tagged payload. Build populates exactly the payload named by Kind.
// BindSecret references an existing immutable version; it never creates a value.
type Change struct {
	Kind       ChangeKind       `json:"kind"`
	Allocation *PortAllocation  `json:"allocation,omitempty"`
	Image      *target.Image    `json:"image,omitempty"`
	Secret     *SecretBinding   `json:"secret,omitempty"`
	Quadlet    *Quadlet         `json:"quadlet,omitempty"`
	Caddy      *CaddyGeneration `json:"caddy,omitempty"`
	Restart    *Restart         `json:"restart,omitempty"`
}
type PortAllocation struct {
	App  string      `json:"app"`
	Port target.Port `json:"port"`
}
type Quadlet struct {
	Desired  policy.Desired  `json:"desired"`
	HostPort target.Port     `json:"host_port"`
	Secrets  []SecretBinding `json:"secrets"`
}
type CaddyGeneration struct {
	Previous uint64             `json:"previous"`
	Next     uint64             `json:"next"`
	Preserve []target.CaddyFile `json:"preserve"`
	App      string             `json:"app"`
	Domains  []spec.Domain      `json:"domains"`
	HostPort target.Port        `json:"host_port"`
}
type Restart struct {
	App string `json:"app"`
}

type Plan struct {
	SchemaVersion      int                        `json:"schema_version"`
	Kind               Kind                       `json:"kind"`
	App                string                     `json:"app"`
	Target             target.Identity            `json:"target"`
	ObservedGeneration target.Observation[uint64] `json:"observed_generation"`
	PolicyVersion      string                     `json:"policy_version"`
	PolicyHash         string                     `json:"policy_hash"`
	DesiredHash        string                     `json:"desired_hash"`
	ConfigHash         string                     `json:"config_hash"`
	Image              target.Image               `json:"image"`
	HostPort           target.Port                `json:"host_port"`
	Secrets            []SecretBinding            `json:"secrets"`
	Changes            []Change                   `json:"changes"`
	Conflicts          []Diagnostic               `json:"conflicts"`
	Hash               string                     `json:"hash"`
}

// CanonicalBytes returns compact JSON with a trailing newline. Build already
// puts every set and ordered change in canonical order. Callers must not mutate
// a returned Plan before serializing or storing it under its Hash.
func (p Plan) CanonicalBytes() ([]byte, error) {
	b, e := json.Marshal(p)
	if e != nil {
		return nil, e
	}
	return append(b, '\n'), nil
}

// Build accepts policy-normalized desired state and measured facts. Malformed
// observation shapes return an error. Unknown or unsupported facts produce a
// conflict plan with no changes, so missing inventory never means a free host.
func Build(in Input) (Plan, error) {
	desired, e := in.Desired.CanonicalBytes()
	if e != nil {
		return Plan{}, e
	}
	snapshot, e := target.Encode(in.Snapshot)
	if e != nil {
		return Plan{}, e
	}
	// Own all output memory and reuse upstream canonical set ordering.
	in.Desired = policy.Desired{}
	if e = json.Unmarshal(desired, &in.Desired); e != nil {
		return Plan{}, e
	}
	in.Snapshot = target.Snapshot{}
	if e = json.Unmarshal(snapshot, &in.Snapshot); e != nil {
		return Plan{}, e
	}
	evidence, e := canonicalEvidence(in.Evidence)
	if e != nil {
		return Plan{}, e
	}
	in.Evidence = Evidence{}
	if e = json.Unmarshal(evidence, &in.Evidence); e != nil {
		return Plan{}, e
	}
	if !validHash(in.Image.Digest) || !strings.HasSuffix(string(in.Desired.Image), "@"+in.Image.Digest) {
		return Plan{}, fmt.Errorf("image metadata must match pinned desired digest")
	}
	if in.Desired.AppPorts.Min < 1024 || in.Desired.AppPorts.Max < in.Desired.AppPorts.Min || in.Desired.PolicyVersion == "" || !validHash(in.Desired.PolicyHash) {
		return Plan{}, fmt.Errorf("normalized policy desired state required")
	}
	p := Plan{SchemaVersion: SchemaVersion, App: string(in.Desired.Name), Target: in.Snapshot.Identity, ObservedGeneration: in.Snapshot.Generation, PolicyVersion: in.Desired.PolicyVersion, PolicyHash: in.Desired.PolicyHash, DesiredHash: hash(desired), Image: in.Image, Secrets: []SecretBinding{}, Changes: []Change{}, Conflicts: []Diagnostic{}}
	add := func(code ConflictCode, field string) {
		p.Conflicts = append(p.Conflicts, Diagnostic{Code: code, Field: field})
	}
	if err := in.Snapshot.Validate(); err != nil {
		var unsupported *target.UnsupportedError
		if !errors.As(err, &unsupported) {
			return Plan{}, err
		}
		add(UnsupportedTarget, "target")
	}
	if in.Image.Platform.OS != "linux" || in.Image.Platform.Arch != in.Snapshot.Arch {
		add(ImagePlatform, "image.platform")
	}
	statuses := []struct {
		field  string
		status target.Status
	}{
		{"generation", in.Snapshot.Generation.Status}, {"apps", in.Snapshot.Apps.Status}, {"used_ports", in.Snapshot.UsedPorts.Status}, {"caddy_config", in.Snapshot.CaddyConfig.Status}, {"routes", in.Evidence.Routes.Status}, {"listeners", in.Evidence.Listeners.Status}, {"applied", in.Evidence.Applied.Status},
	}
	for _, f := range statuses {
		if f.status == target.Unsupported {
			add(UnsupportedTarget, f.field)
		} else if f.status != target.KnownStatus && !(f.field == "caddy_config" && f.status == target.Absent) {
			add(UnknownFacts, f.field)
		}
	}
	if len(p.Conflicts) > 0 {
		return finish(p, desired, snapshot, evidence)
	}
	var current *target.App
	for _, a := range *in.Snapshot.Apps.Value {
		if a.Name == p.App {
			current = &a
			break
		}
	}
	for _, route := range *in.Evidence.Routes.Value {
		if slices.Contains(in.Desired.Domains, route.Domain) && route.App != p.App {
			add(DomainOwned, "domains")
		}
	}
	allocated := false
	if current != nil {
		for _, f := range []struct {
			field  string
			status target.Status
		}{{"app.port", current.AllocatedHostPort.Status}, {"app.release", current.CurrentRelease.Status}, {"app.image", current.Image.Status}, {"app.units", current.QuadletUnits.Status}, {"app.secrets", current.Secrets.Status}} {
			if f.status == target.Unsupported {
				add(UnsupportedTarget, f.field)
			} else if f.status == target.Unknown {
				add(UnknownFacts, f.field)
			}
		}
		if current.AllocatedHostPort.Status == target.KnownStatus {
			p.HostPort = *current.AllocatedHostPort.Value
		}
	}
	if p.HostPort != 0 {
		// UsedPorts has no attribution. Require explicit ownership even if a control
		// record claims the port, since a foreign process may have taken it over.
		owned := false
		for _, l := range *in.Evidence.Listeners.Value {
			if l.Port == p.HostPort {
				if l.App != p.App {
					add(PortOwned, "host_port")
				} else {
					owned = true
				}
			}
		}
		if slices.Contains(*in.Snapshot.UsedPorts.Value, p.HostPort) && !owned {
			add(PortOwned, "host_port")
		}
	} else {
		busy := map[target.Port]bool{}
		for _, port := range *in.Snapshot.UsedPorts.Value {
			busy[port] = true
		}
		for _, l := range *in.Evidence.Listeners.Value {
			busy[l.Port] = true
		}
		for _, a := range *in.Snapshot.Apps.Value {
			if a.AllocatedHostPort.Status == target.KnownStatus {
				busy[*a.AllocatedHostPort.Value] = true
			} else if a.AllocatedHostPort.Status == target.Unknown {
				add(UnknownFacts, "apps.port")
			} else if a.AllocatedHostPort.Status == target.Unsupported {
				add(UnsupportedTarget, "apps.port")
			}
		}
		for port := uint32(in.Desired.AppPorts.Min); port <= uint32(in.Desired.AppPorts.Max); port++ {
			if !busy[target.Port(port)] {
				p.HostPort = target.Port(port)
				allocated = true
				break
			}
		}
		if p.HostPort == 0 {
			add(PortsExhausted, "host_port")
		}
	}
	for _, secret := range in.Desired.Secrets {
		binding := SecretBinding{Environment: secret.Name, Reference: secret.Reference}
		var latest uint64
		prefix := "brine-" + p.App + "-" + string(secret.Reference) + "-v"
		if current != nil && current.Secrets.Status == target.KnownStatus {
			for _, s := range *current.Secrets.Value {
				if !strings.HasPrefix(s.Name, prefix) {
					continue
				}
				version, err := strconv.ParseUint(strings.TrimPrefix(s.Name, prefix), 10, 64)
				if err == nil && version > latest && s.Name == prefix+strconv.FormatUint(version, 10) {
					latest = version
					binding.VersionName = s.Name
					binding.ID = s.ID
				}
			}
		}
		if latest == 0 {
			add(SecretMissing, "secrets."+secret.Name)
		} else {
			p.Secrets = append(p.Secrets, binding)
		}
	}
	caddy := target.CaddyConfigSet{Files: []target.CaddyFile{}}
	if in.Snapshot.CaddyConfig.Status == target.KnownStatus {
		caddy = *in.Snapshot.CaddyConfig.Value
	}
	ownFile := p.App + ".caddy"
	ownHash := ""
	preserve := []target.CaddyFile{}
	for _, file := range caddy.Files {
		if file.Name == ownFile {
			ownHash = file.Hash
			if current == nil {
				add(ArtifactDrift, "caddy.ownership")
			}
		} else {
			preserve = append(preserve, file)
		}
	}
	p.ConfigHash = hashJSON(struct {
		Desired json.RawMessage `json:"desired"`
		Image   target.Image    `json:"image"`
		Port    target.Port     `json:"port"`
		Secrets []SecretBinding `json:"secrets"`
	}{desired, in.Image, p.HostPort, p.Secrets})
	var applied *Applied
	for _, a := range *in.Evidence.Applied.Value {
		if a.App == p.App {
			applied = &a
			break
		}
	}
	if current != nil && applied != nil {
		if current.QuadletUnits.Status != target.KnownStatus || !reflect.DeepEqual(*current.QuadletUnits.Value, applied.Units) || ownHash != applied.CaddyHash {
			add(ArtifactDrift, "artifacts")
		}
	}
	if len(p.Conflicts) > 0 {
		return finish(p, desired, snapshot, evidence)
	}
	p.Kind = Create
	if current != nil && current.CurrentRelease.Status == target.KnownStatus {
		p.Kind = Update
	}
	if p.Kind == Update && applied != nil && applied.ConfigHash == p.ConfigHash && current.Image.Status == target.KnownStatus && *current.Image.Value == in.Image && !allocated && ownHash != "" && hasContainer(*current.QuadletUnits.Value, p.App) {
		p.Kind = NoOp
	} else {
		if caddy.Generation == ^uint64(0) {
			return Plan{}, fmt.Errorf("Caddy generation exhausted")
		}
		if allocated {
			p.Changes = append(p.Changes, Change{Kind: AllocatePort, Allocation: &PortAllocation{App: p.App, Port: p.HostPort}})
		}
		p.Changes = append(p.Changes, Change{Kind: PullImage, Image: &p.Image})
		for _, s := range p.Secrets {
			p.Changes = append(p.Changes, Change{Kind: BindSecret, Secret: &s})
		}
		p.Changes = append(p.Changes, Change{Kind: RenderQuadlet, Quadlet: &Quadlet{Desired: in.Desired, HostPort: p.HostPort, Secrets: slices.Clone(p.Secrets)}}, Change{Kind: StageCaddy, Caddy: &CaddyGeneration{Previous: caddy.Generation, Next: caddy.Generation + 1, Preserve: preserve, App: p.App, Domains: slices.Clone(in.Desired.Domains), HostPort: p.HostPort}}, Change{Kind: RestartApp, Restart: &Restart{App: p.App}})
	}
	return finish(p, desired, snapshot, evidence)
}

func validHash(s string) bool {
	if len(s) != 71 || !strings.HasPrefix(s, "sha256:") {
		return false
	}
	_, e := hex.DecodeString(s[7:])
	return e == nil && strings.ToLower(s) == s
}
func hash(b []byte) string { sum := sha256.Sum256(b); return "sha256:" + hex.EncodeToString(sum[:]) }
func hashJSON(v any) string {
	b, e := json.Marshal(v)
	if e != nil {
		panic(e)
	}
	return hash(b)
}
func finish(p Plan, desired, snapshot, evidence []byte) (Plan, error) {
	if len(p.Conflicts) > 0 {
		p.Kind = Conflict
		p.Changes = []Change{}
	}
	slices.SortFunc(p.Conflicts, func(a, b Diagnostic) int {
		if n := strings.Compare(string(a.Code), string(b.Code)); n != 0 {
			return n
		}
		return strings.Compare(a.Field, b.Field)
	})
	p.Conflicts = slices.Compact(p.Conflicts)
	// Hash the complete hashless output as well as canonical input facts. A plan
	// kind, diagnostic or payload change cannot keep a previous fingerprint.
	p.Hash = hashJSON(struct {
		Desired  json.RawMessage `json:"desired"`
		Snapshot json.RawMessage `json:"snapshot"`
		Evidence json.RawMessage `json:"evidence"`
		Plan     Plan            `json:"plan"`
	}{desired, snapshot, evidence, p})
	return p, nil
}

func canonicalEvidence(e Evidence) ([]byte, error) {
	for _, o := range []struct {
		status target.Status
		value  any
	}{{e.Routes.Status, e.Routes.Value}, {e.Listeners.Status, e.Listeners.Value}, {e.Applied.Status, e.Applied.Value}} {
		present := !reflect.ValueOf(o.value).IsNil()
		if o.status == target.KnownStatus {
			if !present || reflect.ValueOf(o.value).Elem().IsNil() {
				return nil, fmt.Errorf("known evidence requires an array")
			}
		} else if (o.status != target.Unknown && o.status != target.Unsupported) || present {
			return nil, fmt.Errorf("invalid evidence observation")
		}
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	e = Evidence{}
	if err = json.Unmarshal(raw, &e); err != nil {
		return nil, err
	}

	if e.Routes.Value != nil {
		for _, r := range *e.Routes.Value {
			if r.Domain == "" {
				return nil, fmt.Errorf("route requires a normalized domain")
			}
		}
		slices.SortFunc(*e.Routes.Value, func(a, b Route) int {
			if n := strings.Compare(string(a.Domain), string(b.Domain)); n != 0 {
				return n
			}
			return strings.Compare(a.App, b.App)
		})
	}
	if e.Listeners.Value != nil {
		for _, l := range *e.Listeners.Value {
			if l.Port == 0 || l.Port > 65535 {
				return nil, fmt.Errorf("listener port outside 1-65535")
			}
		}
		slices.SortFunc(*e.Listeners.Value, func(a, b Listener) int {
			if a.Port < b.Port {
				return -1
			}
			if a.Port > b.Port {
				return 1
			}
			return strings.Compare(a.App, b.App)
		})
	}
	if e.Applied.Value != nil {
		slices.SortFunc(*e.Applied.Value, func(a, b Applied) int { return strings.Compare(a.App, b.App) })
		for i, a := range *e.Applied.Value {
			if i > 0 && a.App == (*e.Applied.Value)[i-1].App {
				return nil, fmt.Errorf("duplicate applied app")
			}
			if a.Units == nil || !validHash(a.ConfigHash) || (a.CaddyHash != "" && !validHash(a.CaddyHash)) {
				return nil, fmt.Errorf("invalid applied fingerprint")
			}
			slices.SortFunc(a.Units, func(a, b target.Unit) int { return strings.Compare(a.Name, b.Name) })
			for j, unit := range a.Units {
				if unit.Name == "" || !validHash(unit.Hash) || (j > 0 && unit.Name == a.Units[j-1].Name) {
					return nil, fmt.Errorf("invalid applied unit set")
				}
			}
		}
	}
	return json.Marshal(e)
}

func hasContainer(units []target.Unit, app string) bool {
	for _, unit := range units {
		if unit.Name == app+".container" {
			return true
		}
	}
	return false
}
