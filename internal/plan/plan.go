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
	"regexp"
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
	UnsupportedTarget  ConflictCode = "unsupported_target"
	UnknownFacts       ConflictCode = "unknown_facts"
	DomainOwned        ConflictCode = "domain_owned"
	PortOwned          ConflictCode = "port_owned"
	PortsExhausted     ConflictCode = "ports_exhausted"
	ImagePlatform      ConflictCode = "image_platform"
	SecretMissing      ConflictCode = "secret_missing"
	ArtifactDrift      ConflictCode = "artifact_drift"
	RuntimeUnavailable ConflictCode = "runtime_unavailable"
	StaleState         ConflictCode = "stale_brine_state"
	InsufficientDisk   ConflictCode = "insufficient_disk"
)

type Diagnostic struct {
	Code  ConflictCode `json:"code"`
	Field string       `json:"field"`
}

// Image binds the requested index (or single-image) digest to the selected
// platform manifest. Callers without registry or runtime evidence record unknown.
type Image struct {
	Digest         string                     `json:"digest"`
	Platform       target.Platform            `json:"platform"`
	ManifestDigest target.Observation[string] `json:"manifest_digest"`
}

func (i Image) validManifest() bool {
	switch i.ManifestDigest.Status {
	case target.Unknown:
		return i.ManifestDigest.Value == nil
	case target.KnownStatus:
		return i.ManifestDigest.Value != nil && validHash(*i.ManifestDigest.Value)
	default:
		return false
	}
}

func (i Image) observed() target.Image {
	return target.Image{Digest: i.Digest, Platform: i.Platform}
}

type Input struct {
	Desired  policy.Desired
	Snapshot target.Snapshot
	// Image is manifest metadata supplied by the caller, never looked up by Build.
	Image Image
	State BrineState
}

// BrineState comes from the target control database, not host observation. It
// binds committed releases to the snapshot identity and control generation.
// Releases must be non-nil; [] affirmatively records no committed releases.
type BrineState struct {
	Target     target.Identity  `json:"target"`
	Generation uint64           `json:"generation"`
	Releases   []CurrentRelease `json:"releases"`
}

type CurrentRelease struct {
	App       string           `json:"app"`
	ID        string           `json:"id"`
	Desired   policy.Desired   `json:"desired"`
	Image     Image            `json:"image"`
	HostPort  target.Port      `json:"host_port"`
	Secrets   []SecretBinding  `json:"secrets"`
	Units     []target.Unit    `json:"units"`
	CaddyFile target.CaddyFile `json:"caddy_file"`
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
	Image      *Image           `json:"image,omitempty"`
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
	Desired         policy.Desired  `json:"desired"`
	EnvironmentKeys []string        `json:"environment_keys"`
	HostPort        target.Port     `json:"host_port"`
	Secrets         []SecretBinding `json:"secrets"`
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
	Image              Image                      `json:"image"`
	HostPort           target.Port                `json:"host_port"`
	Secrets            []SecretBinding            `json:"secrets"`
	Changes            []Change                   `json:"changes"`
	Diff               *ConfigurationDiff         `json:"diff,omitempty"`
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

// Build accepts policy-normalized desired state, measured host facts and
// committed Brine releases. Required unknown or incompatible facts produce a
// conflict plan with no changes. Malformed input produces no plan and an error.
func Build(in Input) (Plan, error) {
	desired, e := in.Desired.CanonicalBytes()
	if e != nil {
		return Plan{}, e
	}
	snapshot, e := target.Encode(in.Snapshot)
	if e != nil {
		return Plan{}, e
	}
	in.Desired = policy.Desired{}
	if e = json.Unmarshal(desired, &in.Desired); e != nil {
		return Plan{}, e
	}
	in.Snapshot = target.Snapshot{}
	if e = json.Unmarshal(snapshot, &in.Snapshot); e != nil {
		return Plan{}, e
	}
	snapshot, e = canonicalDecisionFacts(in.Snapshot, in.Desired.MinimumFreeDiskBytes)
	if e != nil {
		return Plan{}, e
	}
	state, e := canonicalState(in.State)
	if e != nil {
		return Plan{}, e
	}
	in.State = BrineState{}
	if e = json.Unmarshal(state, &in.State); e != nil {
		return Plan{}, e
	}
	if !in.Image.validManifest() {
		return Plan{}, fmt.Errorf("image requires a known digest or unknown manifest observation")
	}
	if in.Image.ManifestDigest.Value != nil {
		in.Image.ManifestDigest = target.Known(*in.Image.ManifestDigest.Value)
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
	for _, f := range []struct {
		field  string
		status target.Status
	}{
		{"generation", in.Snapshot.Generation.Status}, {"apps", in.Snapshot.Apps.Status}, {"used_ports", in.Snapshot.UsedPorts.Status}, {"caddy_config", in.Snapshot.CaddyConfig.Status}, {"live_caddy_files", in.Snapshot.LiveCaddyFiles.Status}, {"port_owners", in.Snapshot.PortOwners.Status}, {"free_disk_bytes", in.Snapshot.FreeDiskBytes.Status},
	} {
		if f.status == target.Unsupported {
			add(UnsupportedTarget, f.field)
		} else if f.status != target.KnownStatus && !(f.field == "caddy_config" && f.status == target.Absent) {
			add(UnknownFacts, f.field)
		}
	}
	if disk := sufficientDisk(in.Snapshot, in.Desired.MinimumFreeDiskBytes); disk.Status == target.KnownStatus && !*disk.Value {
		add(InsufficientDisk, "free_disk_bytes")
	}
	deploymentCapabilities(in.Snapshot, add)
	if in.State.Target != p.Target || (p.ObservedGeneration.Status == target.KnownStatus && in.State.Generation != *p.ObservedGeneration.Value) {
		add(StaleState, "brine_state")
	}
	if len(p.Conflicts) > 0 {
		return finish(p, desired, snapshot, state)
	}
	var release *CurrentRelease
	for _, r := range in.State.Releases {
		if r.App == p.App {
			release = &r
			break
		}
	}
	var current *target.App
	for _, a := range *in.Snapshot.Apps.Value {
		if a.Name == p.App {
			current = &a
			break
		}
	}
	ownFile := p.App + ".caddy"
	for _, file := range *in.Snapshot.LiveCaddyFiles.Value {
		if file.Domains.Status != target.KnownStatus {
			if file.Domains.Status == target.Unsupported {
				add(UnsupportedTarget, "live_caddy_files.domains")
			} else {
				add(UnknownFacts, "live_caddy_files.domains")
			}
			continue
		}
		for _, observed := range *file.Domains.Value {
			for _, domain := range in.Desired.Domains {
				if servesDomain(observed, string(domain)) && (file.App != p.App || release == nil || file.Name != release.CaddyFile.Name) {
					add(DomainOwned, "domains")
				}
			}
		}
	}
	allocated := false
	if current != nil {
		for _, f := range []struct {
			field  string
			status target.Status
		}{{"app.port", current.AllocatedHostPort.Status}, {"app.image", current.Image.Status}, {"app.units", current.QuadletUnits.Status}, {"app.secrets", current.Secrets.Status}} {
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
		owned := false
		for _, owner := range *in.Snapshot.PortOwners.Value {
			if owner.Port == p.HostPort {
				if owner.App != p.App {
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
		for _, owner := range *in.Snapshot.PortOwners.Value {
			busy[owner.Port] = true
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
	ownHash := ""
	preserve := []target.CaddyFile{}
	for _, file := range caddy.Files {
		if file.Name == ownFile {
			ownHash = file.Hash
			if release == nil || release.CaddyFile.Name != file.Name {
				add(ArtifactDrift, "caddy.ownership")
			}
		} else {
			preserve = append(preserve, file)
		}
	}
	p.ConfigHash = configHash(in.Desired, in.Image, p.HostPort, p.Secrets)

	if release != nil {
		reconcileCommittedCaddy(*release, caddy.Files, *in.Snapshot.LiveCaddyFiles.Value, add)
		if current != nil && current.AllocatedHostPort.Status == target.KnownStatus && *current.AllocatedHostPort.Value != release.HostPort {
			add(ArtifactDrift, "host_port")
		}
		if current == nil || current.QuadletUnits.Status != target.KnownStatus || !reflect.DeepEqual(*current.QuadletUnits.Value, release.Units) {
			add(ArtifactDrift, "units")
		}
		if current != nil && current.Image.Status == target.KnownStatus && *current.Image.Value != release.Image.observed() {
			add(ArtifactDrift, "image")
		}
	} else if current != nil && ((current.Image.Status == target.KnownStatus) || (current.QuadletUnits.Status == target.KnownStatus && len(*current.QuadletUnits.Value) > 0)) {
		add(ArtifactDrift, "app.ownership")
	}
	if len(p.Conflicts) > 0 {
		return finish(p, desired, snapshot, state)
	}
	p.Kind = Create
	if release != nil {
		p.Kind = Update
	}
	if release != nil && configHash(release.Desired, release.Image, release.HostPort, release.Secrets) == p.ConfigHash && current.Image.Status == target.KnownStatus && *current.Image.Value == in.Image.observed() && !allocated && ownHash != "" && hasContainer(*current.QuadletUnits.Value, p.App) {
		p.Kind = NoOp
	} else {
		for _, committed := range in.State.Releases {
			if committed.App != p.App {
				reconcileCommittedCaddy(committed, caddy.Files, *in.Snapshot.LiveCaddyFiles.Value, add)
			}
		}
		if len(p.Conflicts) > 0 {
			return finish(p, desired, snapshot, state)
		}
		if caddy.Generation == ^uint64(0) {
			return Plan{}, fmt.Errorf("Caddy generation exhausted")
		}
		if allocated {
			p.Changes = append(p.Changes, Change{Kind: AllocatePort, Allocation: &PortAllocation{App: p.App, Port: p.HostPort}})
		}
		p.Changes = append(p.Changes, Change{Kind: PullImage, Image: &p.Image})
		for _, binding := range p.Secrets {
			p.Changes = append(p.Changes, Change{Kind: BindSecret, Secret: &binding})
		}
		p.Diff = configurationDiff(in.Desired, in.Image, p.HostPort, p.Secrets, release)
		quadletDesired := in.Desired
		quadletDesired.Environment = []policy.Environment{}
		environmentKeys := make([]string, 0, len(in.Desired.Environment))
		for _, e := range in.Desired.Environment {
			environmentKeys = append(environmentKeys, e.Name)
		}
		p.Changes = append(p.Changes, Change{Kind: RenderQuadlet, Quadlet: &Quadlet{Desired: quadletDesired, EnvironmentKeys: environmentKeys, HostPort: p.HostPort, Secrets: slices.Clone(p.Secrets)}}, Change{Kind: StageCaddy, Caddy: &CaddyGeneration{Previous: caddy.Generation, Next: caddy.Generation + 1, Preserve: preserve, App: p.App, Domains: slices.Clone(in.Desired.Domains), HostPort: p.HostPort}}, Change{Kind: RestartApp, Restart: &Restart{App: p.App}})
	}
	return finish(p, desired, snapshot, state)
}

func reconcileCommittedCaddy(release CurrentRelease, files []target.CaddyFile, liveFiles []target.LiveCaddyFile, add func(ConflictCode, string)) {
	artifactMatches := false
	for _, file := range files {
		if file == release.CaddyFile {
			artifactMatches = true
			break
		}
	}
	if !artifactMatches {
		add(ArtifactDrift, "caddy")
	}
	expectedDomains := make([]string, len(release.Desired.Domains))
	for i, domain := range release.Desired.Domains {
		expectedDomains[i] = string(domain)
	}
	liveMatches := false
	for _, file := range liveFiles {
		if file.Name == release.CaddyFile.Name && file.App == release.App && file.Domains.Status == target.KnownStatus && slices.Equal(*file.Domains.Value, expectedDomains) {
			liveMatches = true
			break
		}
	}
	if !liveMatches {
		add(ArtifactDrift, "caddy.live")
	}
}

func deploymentCapabilities(s target.Snapshot, add func(ConflictCode, string)) {
	for _, r := range []struct {
		field    string
		value    target.Observation[string]
		baseline string
	}{
		{"versions.systemd", s.Versions.Systemd, "257"}, {"versions.podman", s.Versions.Podman, "5.4"}, {"versions.caddy", s.Versions.Caddy, "2.6"}, {"versions.passt", s.Versions.Passt, ""}, {"runner.user", s.Runner.User, ""},
	} {
		switch r.value.Status {
		case target.Unknown:
			add(UnknownFacts, r.field)
		case target.Unsupported:
			add(UnsupportedTarget, r.field)
		case target.Absent:
			add(RuntimeUnavailable, r.field)
		case target.KnownStatus:
			if r.baseline != "" && !versionLine(*r.value.Value, r.baseline) {
				add(RuntimeUnavailable, r.field)
			}
			if r.field == "versions.passt" && !passtVersion.MatchString(*r.value.Value) {
				add(RuntimeUnavailable, r.field)
			}
			if r.field == "runner.user" && *r.value.Value == "root" {
				add(RuntimeUnavailable, r.field)
			}
		}
	}
	for _, r := range []struct {
		field string
		value target.Observation[bool]
	}{{"cgroup_v2", s.CgroupV2}, {"runner.linger", s.Runner.Linger}} {
		switch r.value.Status {
		case target.Unknown:
			add(UnknownFacts, r.field)
		case target.Unsupported:
			add(UnsupportedTarget, r.field)
		case target.KnownStatus:
			if !*r.value.Value {
				add(RuntimeUnavailable, r.field)
			}
		}
	}
}

var passtVersion = regexp.MustCompile(`^0\.0~git[0-9]{8}\.[0-9a-f]+([+~.-][0-9A-Za-z.+~_-]+)?$`)

func versionLine(value, baseline string) bool {
	return value == baseline || strings.HasPrefix(value, baseline+".") || strings.HasPrefix(value, baseline+"-") || strings.HasPrefix(value, baseline+"+") || strings.HasPrefix(value, baseline+"~")
}
func servesDomain(observed, desired string) bool {
	if observed == "*" || observed == desired {
		return true
	}
	if strings.HasPrefix(observed, "*.") {
		suffix := observed[1:]
		return strings.HasSuffix(desired, suffix) && !strings.Contains(strings.TrimSuffix(desired, suffix), ".")
	}
	return false
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
func configHash(d policy.Desired, image Image, port target.Port, secrets []SecretBinding) string {
	desired, e := d.CanonicalBytes()
	if e != nil {
		panic(e)
	}
	return hashJSON(struct {
		Desired json.RawMessage `json:"desired"`
		Image   Image           `json:"image"`
		Port    target.Port     `json:"port"`
		Secrets []SecretBinding `json:"secrets"`
	}{desired, image, port, secrets})
}
func finish(p Plan, desired, snapshot, state []byte) (Plan, error) {
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
	p.Hash = hashJSON(struct {
		Desired  json.RawMessage `json:"desired"`
		Snapshot json.RawMessage `json:"snapshot"`
		State    json.RawMessage `json:"brine_state"`
		Plan     Plan            `json:"plan"`
	}{desired, snapshot, state, p})
	return p, nil
}
func canonicalState(state BrineState) ([]byte, error) {
	if state.Releases == nil {
		return nil, fmt.Errorf("Brine state requires a release array")
	}
	raw, e := json.Marshal(state)
	if e != nil {
		return nil, e
	}
	state = BrineState{}
	if e = json.Unmarshal(raw, &state); e != nil {
		return nil, e
	}
	slices.SortFunc(state.Releases, func(a, b CurrentRelease) int { return strings.Compare(a.App, b.App) })
	for i, r := range state.Releases {
		if r.App == "" || r.ID == "" || string(r.Desired.Name) != r.App || (i > 0 && state.Releases[i-1].App == r.App) || !validHash(r.Image.Digest) || !r.Image.validManifest() || r.HostPort == 0 || r.HostPort > 65535 || r.Units == nil || r.Secrets == nil || r.CaddyFile.Name != r.App+".caddy" || !validHash(r.CaddyFile.Hash) {
			return nil, fmt.Errorf("invalid committed release state")
		}
		desired, e := r.Desired.CanonicalBytes()
		if e != nil {
			return nil, e
		}
		state.Releases[i].Desired = policy.Desired{}
		if e = json.Unmarshal(desired, &state.Releases[i].Desired); e != nil {
			return nil, e
		}
		slices.SortFunc(r.Units, func(a, b target.Unit) int { return strings.Compare(a.Name, b.Name) })
		for j, unit := range r.Units {
			if unit.Name == "" || !validHash(unit.Hash) || (j > 0 && unit.Name == r.Units[j-1].Name) {
				return nil, fmt.Errorf("invalid committed unit set")
			}
		}
		slices.SortFunc(r.Secrets, func(a, b SecretBinding) int { return strings.Compare(a.Environment, b.Environment) })
		for j, binding := range r.Secrets {
			if binding.Environment == "" || binding.Reference == "" || binding.ID == "" || !strings.HasPrefix(binding.VersionName, "brine-"+r.App+"-"+string(binding.Reference)+"-v") || (j > 0 && binding.Environment == r.Secrets[j-1].Environment) {
				return nil, fmt.Errorf("invalid committed secret binding")
			}
		}
	}
	return json.Marshal(state)
}
func hasContainer(units []target.Unit, app string) bool {
	for _, unit := range units {
		if unit.Name == app+".container" {
			return true
		}
	}
	return false
}
