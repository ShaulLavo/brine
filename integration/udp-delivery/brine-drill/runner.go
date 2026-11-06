// Package brinedrill runs the source-only UDP delivery drill from typed evidence.
package brinedrill

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/fixtures/udp-echo/client"
	"github.com/ShaulLavo/brine/fixtures/udp-echo/protocol"
	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
)

const (
	ManifestSchema = 2
	LedgerSchema   = 2
	ReceiptSchema  = 1
	maxManifest    = 1 << 20
	maxReceipt     = 1 << 20
	maxOutput      = 4 << 20
)

type Phase string

const (
	PhaseBeforeReboot Phase = "before-reboot"
	PhaseAfterReboot  Phase = "after-reboot"
	PhaseCleanup      Phase = "cleanup"
	PhaseInspect      Phase = "inspect"
	PhaseResume       Phase = "resume"
)

type Action string

const (
	ActionPlan          Action = "plan"
	ActionApply         Action = "apply"
	ActionStatus        Action = "status"
	ActionLifecycle     Action = "lifecycle"
	ActionRollback      Action = "rollback"
	ActionProbe         Action = "client-probe"
	ActionInspect       Action = "inspect"
	ActionResume        Action = "resume"
	ActionTeardownProof Action = "teardown-proof"
)

type Manifest struct {
	Schema              int             `json:"schema"`
	RunID               string          `json:"run_id"`
	Source              SourcePins      `json:"source"`
	Fixture             FixturePins     `json:"fixture"`
	Target              TargetSpec      `json:"target"`
	Apps                []AppSpec       `json:"apps"`
	DependencyReceipts  []ReceiptRef    `json:"dependency_receipts"`
	PhaseReceipts       PhaseReceipts   `json:"phase_receipts"`
	OwnedResources      []OwnedResource `json:"owned_resources"`
	Phases              PhaseSteps      `json:"phases"`
	ReadAttempts        int             `json:"read_attempts,omitempty"`
	ReadDeadlineSeconds int             `json:"read_deadline_seconds,omitempty"`
	OutputBytes         int             `json:"output_bytes,omitempty"`
}

type SourcePins struct {
	Commit       string    `json:"commit"`
	HostBinary   BinaryPin `json:"host_binary"`
	ClientBinary BinaryPin `json:"client_binary"`
}

type BinaryPin struct {
	Path         string `json:"path"`
	SHA256       string `json:"sha256"`
	SourceCommit string `json:"source_commit"`
}

type FixturePins struct {
	ImageIndexDigest string            `json:"image_index_digest"`
	PlatformDigests  map[string]string `json:"platform_digests"`
	ReleaseDigests   map[string]string `json:"release_digests"`
}

type TargetSpec struct {
	Kind                       string `json:"kind"`
	Name                       string `json:"name"`
	ConfigDir                  string `json:"config_dir"`
	ControlledUnreachable      string `json:"controlled_unreachable"`
	ParentControllerReceiptDir string `json:"parent_controller_receipt_dir"`
}

type AppSpec struct {
	Name        string   `json:"name"`
	Domain      string   `json:"domain"`
	Incarnation string   `json:"incarnation"`
	Endpoint    Endpoint `json:"endpoint"`
}

type Endpoint struct {
	BindAddress   string `json:"bind_address"`
	ClientAddress string `json:"client_address"`
	HostPort      uint16 `json:"host_port"`
	ContainerPort uint16 `json:"container_port"`
	Exposure      string `json:"exposure"`
}

type ReceiptRef struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type PhaseReceipts struct {
	BeforeReboot ReceiptRef `json:"before_reboot"`
	AfterReboot  ReceiptRef `json:"after_reboot"`
}

type OwnedResource struct {
	Kind  string `json:"kind"`
	ID    string `json:"id"`
	App   string `json:"app,omitempty"`
	Owner string `json:"owner"`
}

type PhaseSteps struct {
	BeforeReboot []Step `json:"before_reboot"`
	AfterReboot  []Step `json:"after_reboot"`
	Cleanup      []Step `json:"cleanup"`
	Inspect      []Step `json:"inspect"`
	Resume       []Step `json:"resume"`
}

type Step struct {
	ID              string `json:"id"`
	Action          Action `json:"action"`
	App             string `json:"app,omitempty"`
	Spec            string `json:"spec,omitempty"`
	PlanStep        string `json:"plan_step,omitempty"`
	PlanID          string `json:"plan_id,omitempty"`
	releaseID       string
	ReleaseStep     string              `json:"release_step,omitempty"`
	Expectation     *ReleaseExpectation `json:"expectation,omitempty"`
	OperationID     string              `json:"operation_id,omitempty"`
	Proof           string              `json:"proof,omitempty"`
	ProofSHA256     string              `json:"proof_sha256,omitempty"`
	Endpoint        string              `json:"endpoint,omitempty"`
	Vantage         string              `json:"vantage,omitempty"`
	Nonce           string              `json:"nonce,omitempty"`
	ExpectedOutcome string              `json:"expected_outcome,omitempty"`
	LifecycleAction string              `json:"lifecycle_action,omitempty"`
}

type Ledger struct {
	Schema       int              `json:"schema"`
	RunID        string           `json:"run_id"`
	ManifestHash string           `json:"manifest_hash"`
	Entries      []LedgerEntry    `json:"entries"`
	Resources    []LedgerResource `json:"resources"`
}

type LedgerEntry struct {
	ID            string    `json:"id"`
	Action        Action    `json:"action"`
	App           string    `json:"app,omitempty"`
	Key           string    `json:"idempotency_key"`
	PlanID        string    `json:"plan_id,omitempty"`
	ReleaseID     string    `json:"release_id,omitempty"`
	OperationID   string    `json:"operation_id,omitempty"`
	OperationKind ops.Kind  `json:"operation_kind,omitempty"`
	RecoveryOf    string    `json:"recovery_of,omitempty"`
	State         string    `json:"state"`
	CommandSHA256 string    `json:"command_sha256"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type LedgerResource struct {
	Kind  string `json:"kind"`
	ID    string `json:"id"`
	App   string `json:"app,omitempty"`
	State string `json:"state"`
}

type Receipt struct {
	Schema         int    `json:"schema"`
	RunID          string `json:"run_id"`
	Target         string `json:"target"`
	Phase          string `json:"phase"`
	HostProof      string `json:"host_proof"`
	RebootRestored bool   `json:"reboot_restored"`
}

type ProductionProof struct {
	Schema         int          `json:"schema"`
	RunID          string       `json:"run_id"`
	Target         string       `json:"target"`
	Kind           string       `json:"kind"`
	App            string       `json:"app"`
	Endpoint       Endpoint     `json:"endpoint"`
	Release        string       `json:"release"`
	ImageDigest    string       `json:"image_digest"`
	ReleaseID      string       `json:"release_id"`
	Incarnation    string       `json:"incarnation"`
	HTTPHealth     string       `json:"http_health"`
	NetworkMode    string       `json:"network_mode"`
	Mapping        MappingProof `json:"mapping"`
	Socket         *SocketProof `json:"socket,omitempty"`
	InodeGate      string       `json:"inode_gate"`
	ForeignOverlap string       `json:"foreign_overlap"`
	RouteUnchanged bool         `json:"route_unchanged"`
}

type MappingProof struct {
	Present       bool   `json:"present"`
	BindAddress   string `json:"bind_address"`
	HostPort      uint16 `json:"host_port"`
	ContainerPort uint16 `json:"container_port"`
}

type SocketProof struct {
	Address   string `json:"address"`
	Port      uint16 `json:"port"`
	App       string `json:"app"`
	Process   string `json:"process"`
	Unit      string `json:"unit"`
	Inode     uint64 `json:"inode"`
	Source    string `json:"source"`
	Ownership string `json:"ownership"`
}

type TeardownProof struct {
	Schema           int                `json:"schema"`
	RunID            string             `json:"run_id"`
	Target           string             `json:"target"`
	ForeignUntouched bool               `json:"foreign_untouched"`
	Resources        []TeardownResource `json:"resources"`
}

type TeardownResource struct {
	Kind  string `json:"kind"`
	ID    string `json:"id"`
	State string `json:"state"`
}

type CommandSpec struct {
	Binary   string
	Args     []string
	Mutating bool
}

type CommandResult struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

type CommandRunner interface {
	Run(context.Context, CommandSpec, time.Duration, int) (CommandResult, error)
}

type ExecRunner struct {
	Executor localexec.Executor
}

var (
	digestPattern      = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	hashPattern        = regexp.MustCompile(`^[0-9a-f]{64}$`)
	commitPattern      = regexp.MustCompile(`^[0-9a-f]{40}$`)
	namePattern        = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	releasePattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,255}$`)
	domainPattern      = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]{0,251}[a-z0-9])?$`)
	operationIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
)

func (r ExecRunner) Run(ctx context.Context, spec CommandSpec, deadline time.Duration, outputLimit int) (CommandResult, error) {
	if spec.Binary == "" || !filepath.IsAbs(spec.Binary) || deadline <= 0 || outputLimit <= 0 {
		return CommandResult{}, errors.New("invalid command bounds")
	}
	executor := r.Executor
	if executor == nil {
		executor = localexec.ExecRunner{}
	}
	result, err := executor.Execute(ctx, localexec.Command{
		Path:        spec.Binary,
		Args:        append([]string(nil), spec.Args...),
		Timeout:     deadline,
		Mutation:    spec.Mutating,
		OutputLimit: min(outputLimit, localexec.CommandOutputLimit),
	})
	commandResult := CommandResult{Stdout: []byte(result.Stdout), Stderr: []byte(result.Stderr), ExitCode: result.ExitCode}
	if result.Truncated {
		return commandResult, errors.New("command output limit exceeded")
	}
	return commandResult, err
}

type Runner struct {
	Manifest Manifest
	Ledger   LedgerStore
	Commands CommandRunner
	DryRun   bool
}

type LedgerStore interface {
	Load() (Ledger, error)
	Save(Ledger) error
}

type FileLedger struct {
	Path         string
	ManifestHash string
	RunID        string
	Resources    []OwnedResource
}

type RunReport struct {
	Phase  Phase        `json:"phase"`
	DryRun bool         `json:"dry_run"`
	Steps  []StepReport `json:"steps"`
	Ledger Ledger       `json:"ledger"`
}

type StepReport struct {
	ID          string `json:"id"`
	Action      Action `json:"action"`
	State       string `json:"state"`
	PlanID      string `json:"plan_id,omitempty"`
	OperationID string `json:"operation_id,omitempty"`
	Outcome     string `json:"outcome,omitempty"`
}

func LoadManifest(path string) (Manifest, error) {
	raw, err := readBounded(path, maxManifest)
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Manifest{}, errors.New("manifest has trailing data")
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func (m Manifest) Validate() error {
	if m.Schema != ManifestSchema || !namePattern.MatchString(m.RunID) {
		return errors.New("invalid manifest identity")
	}
	if !commitPattern.MatchString(m.Source.Commit) || m.Source.HostBinary.SourceCommit != m.Source.Commit || m.Source.ClientBinary.SourceCommit != m.Source.Commit {
		return errors.New("source commits do not match")
	}
	if err := validateBinary(m.Source.HostBinary); err != nil {
		return err
	}
	if err := validateBinary(m.Source.ClientBinary); err != nil {
		return err
	}
	if !digestPattern.MatchString(m.Fixture.ImageIndexDigest) || len(m.Fixture.PlatformDigests) == 0 || len(m.Fixture.ReleaseDigests) == 0 || len(m.DependencyReceipts) == 0 {
		return errors.New("fixture image pins are incomplete")
	}
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		if !digestPattern.MatchString(m.Fixture.PlatformDigests[platform]) {
			return errors.New("fixture image pins are incomplete")
		}
	}
	for platform, digest := range m.Fixture.PlatformDigests {
		if platform != "linux/amd64" && platform != "linux/arm64" || !digestPattern.MatchString(digest) {
			return errors.New("invalid fixture platform pin")
		}
	}
	for release, digest := range m.Fixture.ReleaseDigests {
		if !releasePattern.MatchString(release) || !digestPattern.MatchString(digest) {
			return errors.New("invalid fixture release pin")
		}
	}
	if err := m.Target.Validate(); err != nil {
		return err
	}
	requiredApps := 1
	if m.Target.Kind == "pi" {
		requiredApps = 2
	}
	if len(m.Apps) != requiredApps {
		return fmt.Errorf("%s requires %d apps", m.Target.Kind, requiredApps)
	}
	seenApps := map[string]bool{}
	for _, app := range m.Apps {
		if err := app.Validate(); err != nil {
			return err
		}
		if seenApps[app.Name] {
			return errors.New("duplicate app")
		}
		seenApps[app.Name] = true
	}
	for _, receipt := range m.DependencyReceipts {
		if err := validateReceiptRef(receipt); err != nil {
			return err
		}
	}
	if err := m.PhaseReceipts.Validate(m.RunID, m.Target.Name); err != nil {
		return err
	}
	if err := validateResources(m.OwnedResources, m.RunID, seenApps); err != nil {
		return err
	}
	if err := m.Phases.Validate(seenApps); err != nil {
		return err
	}
	if _, err := m.releaseExpectations(); err != nil {
		return err
	}
	if err := validateProbeEndpoints(m.Phases, m.Apps, m.Target); err != nil {
		return err
	}
	if m.ReadAttempts == 0 {
		m.ReadAttempts = 3
	}
	if m.ReadAttempts < 1 || m.ReadAttempts > 3 || m.ReadDeadlineSeconds < 0 || m.ReadDeadlineSeconds > 60 || m.OutputBytes < 0 || m.OutputBytes > maxOutput {
		return errors.New("invalid runner bounds")
	}
	return nil
}

func validateBinary(binary BinaryPin) error {
	if binary.Path == "" || !filepath.IsAbs(binary.Path) || !hashPattern.MatchString(binary.SHA256) {
		return errors.New("invalid binary pin")
	}
	got, err := fileSHA256(binary.Path)
	if err != nil || got != binary.SHA256 {
		return errors.New("binary hash does not match")
	}
	return nil
}

func (t TargetSpec) Validate() error {
	if t.Kind != "pi" && t.Kind != "vps" || !namePattern.MatchString(t.Name) || t.ConfigDir == "" || !filepath.IsAbs(t.ConfigDir) || t.ControlledUnreachable == "" {
		return errors.New("invalid target")
	}
	if _, err := netip.ParseAddrPort(t.ControlledUnreachable); err != nil {
		return errors.New("invalid controlled unreachable endpoint")
	}
	return nil
}

func (a AppSpec) Validate() error {
	if !namePattern.MatchString(a.Name) || !domainPattern.MatchString(a.Domain) || !digestPattern.MatchString(a.Incarnation) {
		return errors.New("invalid app identity")
	}

	if err := a.Endpoint.Validate(); err != nil {
		return err
	}
	return nil
}

func (e Endpoint) Validate() error {
	bind, err := netip.ParseAddr(e.BindAddress)
	if err != nil || bind.IsUnspecified() || bind.IsMulticast() || bind.Zone() != "" || bind.String() != e.BindAddress || e.HostPort < 1024 || e.ContainerPort < 1024 || e.Exposure != "public" && e.Exposure != "tailnet" {
		return errors.New("invalid UDP endpoint")
	}
	clientAddress, err := netip.ParseAddr(e.ClientAddress)
	if err != nil || clientAddress.IsUnspecified() || clientAddress.IsMulticast() || clientAddress.Zone() != "" || clientAddress.String() != e.ClientAddress {
		return errors.New("invalid UDP client address")
	}
	if (bind.Is4() && !clientAddress.Is4()) || (bind.Is6() && !clientAddress.Is6()) {
		return errors.New("endpoint address families do not match")
	}
	return nil
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func (r ReceiptRef) Validate() error {
	if r.Name == "" || r.Path == "" || !filepath.IsAbs(r.Path) || !hashPattern.MatchString(r.SHA256) {
		return errors.New("invalid receipt reference")
	}
	got, err := fileSHA256(r.Path)
	if err != nil || got != r.SHA256 {
		return errors.New("receipt hash does not match")
	}
	return nil
}

func validateReceiptRef(r ReceiptRef) error { return r.Validate() }

func (p PhaseReceipts) Validate(runID, target string) error {
	for _, receipt := range []ReceiptRef{p.BeforeReboot, p.AfterReboot} {
		if receipt.Path == "" {
			return errors.New("phase receipt is required")
		}
		if err := receipt.Validate(); err != nil {
			return err
		}
		value, err := loadStrict[Receipt](receipt.Path, maxReceipt)
		if err != nil || value.Schema != ReceiptSchema || value.RunID != runID || value.Target != target || value.HostProof == "" {
			return errors.New("phase receipt is invalid")
		}
	}
	return nil
}

func validateResources(resources []OwnedResource, runID string, apps map[string]bool) error {
	if len(resources) == 0 {
		return errors.New("owned resource ledger is empty")
	}
	seen := map[string]bool{}
	for _, resource := range resources {
		if resource.Kind == "" || resource.ID == "" || resource.Owner != runID || resource.App != "" && !apps[resource.App] {
			return errors.New("invalid owned resource")
		}
		key := resource.Kind + "\x00" + resource.ID
		if seen[key] {
			return errors.New("duplicate owned resource")
		}
		seen[key] = true
	}
	return nil
}

func (p PhaseSteps) Validate(apps map[string]bool) error {
	seen := map[string]bool{}
	for _, step := range p.allSteps() {
		if err := validatePhaseStep(step, apps, seen); err != nil {
			return err
		}
		seen[step.ID] = true
	}
	return validateCleanupSteps(p.Cleanup, apps)
}

func validatePhaseStep(step Step, apps, seen map[string]bool) error {
	if step.ID == "" || seen[step.ID] || step.Action == "" || !validAction(step.Action) || !validApplyOutcome(step) || step.App != "" && !apps[step.App] {
		return errors.New("invalid phase step")
	}
	if step.Action == ActionPlan && (step.Spec == "" || !filepath.IsAbs(step.Spec) || !regularFile(step.Spec)) || step.Action == ActionApply && step.PlanStep == "" && step.PlanID == "" || step.Action == ActionRollback && step.ReleaseStep == "" || step.Action == ActionInspect && step.OperationID == "" || step.Action == ActionResume && step.OperationID == "" || step.Action == ActionTeardownProof && !validProofReference(step) || step.Action == ActionLifecycle && !slices.Contains([]string{"stop", "start", "restart", "remove"}, step.LifecycleAction) || step.Action == ActionProbe && !validProbeStep(step) || step.Action == ActionStatus && slices.Contains([]string{"ready", "absent"}, step.ExpectedOutcome) && !validProofReference(step) {
		return errors.New("phase step is missing required input")
	}
	if strings.Contains(step.ActionString(), "reboot") || strings.Contains(step.ActionString(), "kernel") || strings.Contains(step.ActionString(), "firewall") || strings.Contains(step.ActionString(), "policy") {
		return errors.New("runner cannot own host control actions")
	}
	return nil
}

func validProofReference(step Step) bool {
	if !validProofPath(step) || !hashPattern.MatchString(step.ProofSHA256) {
		return false
	}
	got, err := fileSHA256(step.Proof)
	return err == nil && got == step.ProofSHA256
}

func validProofPath(step Step) bool {
	return step.Proof != "" && filepath.IsAbs(step.Proof) && regularFile(step.Proof)
}

func validProbeStep(step Step) bool {
	if step.Vantage == "" || !slices.Contains([]string{"ok", "timeout"}, step.ExpectedOutcome) || step.Nonce == "" {
		return false
	}
	return protocol.ValidateNonce([]byte(step.Nonce)) == nil
}

func validateProbeEndpoints(phases PhaseSteps, apps []AppSpec, targetSpec TargetSpec) error {
	byName := make(map[string]string, len(apps))
	for _, app := range apps {
		byName[app.Name] = net.JoinHostPort(app.Endpoint.ClientAddress, strconv.Itoa(int(app.Endpoint.HostPort)))
	}
	for _, steps := range [][]Step{phases.BeforeReboot, phases.AfterReboot, phases.Cleanup, phases.Inspect, phases.Resume} {
		for _, step := range steps {
			if step.Action != ActionProbe {
				continue
			}
			appEndpoint, ok := byName[step.App]
			if !ok {
				return errors.New("probe step names an unknown app")
			}
			endpoint := step.Endpoint
			if endpoint == "" {
				endpoint = appEndpoint
			}
			if endpoint != appEndpoint && endpoint != targetSpec.ControlledUnreachable {
				return errors.New("probe endpoint is not approved")
			}
			if endpoint == appEndpoint && step.ExpectedOutcome != "ok" || endpoint == targetSpec.ControlledUnreachable && step.ExpectedOutcome != "timeout" {
				return errors.New("probe outcome does not match approved endpoint")
			}
		}
	}
	return nil
}

func validateCleanupSteps(steps []Step, apps map[string]bool) error {
	if len(steps) < 2 || steps[len(steps)-1].Action != ActionTeardownProof {
		return errors.New("cleanup must remove owned apps before teardown proof")
	}
	removed := make(map[string]bool, len(apps))
	for _, step := range steps[:len(steps)-1] {
		if step.Action != ActionLifecycle || step.LifecycleAction != "remove" || step.App == "" {
			return errors.New("cleanup accepts only owned removal steps before teardown proof")
		}
		removed[step.App] = true
	}
	if len(removed) != len(apps) {
		return errors.New("cleanup must remove every declared app before teardown proof")
	}
	return nil
}

func validApplyOutcome(step Step) bool {
	return step.Action != ActionApply || slices.Contains([]string{"", "succeeded", "failed", "rolled_back"}, step.ExpectedOutcome)
}

func (s Step) ActionString() string { return string(s.Action) }

func validAction(action Action) bool {
	return slices.Contains([]Action{ActionPlan, ActionApply, ActionStatus, ActionLifecycle, ActionRollback, ActionProbe, ActionInspect, ActionResume, ActionTeardownProof}, action)
}

func (f FileLedger) Load() (Ledger, error) {
	if _, err := os.Stat(f.Path); errors.Is(err, os.ErrNotExist) {
		return Ledger{Schema: LedgerSchema, RunID: f.RunID, ManifestHash: f.ManifestHash, Entries: []LedgerEntry{}, Resources: resourcesFor(f.Resources)}, nil
	} else if err != nil {
		return Ledger{}, err
	}
	ledger, err := loadStrict[Ledger](f.Path, maxReceipt)
	if err != nil {
		return Ledger{}, err
	}
	if ledger.Schema != LedgerSchema || ledger.RunID != f.RunID || ledger.ManifestHash != f.ManifestHash {
		return Ledger{}, errors.New("ledger identity does not match manifest")
	}
	return ledger, nil
}

func (f FileLedger) Save(ledger Ledger) error {
	if ledger.Schema != LedgerSchema || ledger.RunID != f.RunID || ledger.ManifestHash != f.ManifestHash {
		return errors.New("refusing to save foreign ledger")
	}
	if err := os.MkdirAll(filepath.Dir(f.Path), 0700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(ledger, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(f.Path), ".ledger-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer func() { _ = os.Remove(name) }()
	if err := temporary.Chmod(0600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(raw); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(name, f.Path)
}

func resourcesFor(resources []OwnedResource) []LedgerResource {
	out := make([]LedgerResource, 0, len(resources))
	for _, resource := range resources {
		out = append(out, LedgerResource{Kind: resource.Kind, ID: resource.ID, App: resource.App, State: "planned"})
	}
	return out
}

func (r *Runner) RunPhase(ctx context.Context, phase Phase) (RunReport, error) {
	if err := r.Manifest.Validate(); err != nil {
		return RunReport{}, err
	}
	if r.Commands == nil {
		r.Commands = ExecRunner{}
	}
	if r.Ledger == nil {
		return RunReport{}, errors.New("ledger is required")
	}
	steps := r.Manifest.Phases.steps(phase)
	if steps == nil {
		return RunReport{}, errors.New("phase is not defined")
	}
	if !r.DryRun {
		if err := r.checkPhaseGate(phase); err != nil {
			return RunReport{}, err
		}
	}
	if r.DryRun {
		return r.dryRun(phase, steps)
	}
	ledger, err := r.Ledger.Load()
	if err != nil {
		return RunReport{}, err
	}
	report := RunReport{Phase: phase, Steps: []StepReport{}, Ledger: ledger}
	for _, step := range steps {
		result, err := r.runStep(ctx, &ledger, step)
		report.Steps = append(report.Steps, result)
		if err != nil {
			report.Ledger = ledger
			return report, err
		}
	}
	report.Ledger = ledger
	return report, nil
}

func (p PhaseSteps) steps(phase Phase) []Step {
	switch phase {
	case PhaseBeforeReboot:
		return p.BeforeReboot
	case PhaseAfterReboot:
		return p.AfterReboot
	case PhaseCleanup:
		return p.Cleanup
	case PhaseInspect:
		return p.Inspect
	case PhaseResume:
		return p.Resume
	default:
		return nil
	}
}

func (r *Runner) checkPhaseGate(phase Phase) error {
	if phase == PhaseBeforeReboot {
		return validatePhaseReceipt(r.Manifest.PhaseReceipts.BeforeReboot, r.Manifest.RunID, r.Manifest.Target.Name, "before-reboot", false)
	}
	if phase == PhaseAfterReboot {
		return validatePhaseReceipt(r.Manifest.PhaseReceipts.AfterReboot, r.Manifest.RunID, r.Manifest.Target.Name, "after-reboot", true)
	}
	return nil
}

func validatePhaseReceipt(reference ReceiptRef, runID, target, phase string, requireReboot bool) error {
	value, err := loadStrict[Receipt](reference.Path, maxReceipt)
	if err != nil || value.Schema != ReceiptSchema || value.RunID != runID || value.Target != target || value.Phase != phase || value.HostProof == "" || requireReboot && !value.RebootRestored {
		return errors.New("phase gate receipt is not accepted")
	}
	return nil
}

func (r *Runner) commandFor(step Step, ledger Ledger) (CommandSpec, error) {
	switch step.Action {
	case ActionPlan:
		return r.planCommand(step), nil
	case ActionApply:
		planID, err := r.planReference(ledger, step)
		if err != nil && step.PlanID == "" {
			return CommandSpec{}, err
		}
		if step.PlanID != "" {
			planID = step.PlanID
		}
		return r.applyCommand(planID, operationKey(r.Manifest.RunID, step.ID)), nil
	case ActionStatus:
		return r.statusCommand(step.App), nil
	case ActionLifecycle:
		return CommandSpec{Binary: r.Manifest.Source.HostBinary.Path, Args: r.baseArgs(step.LifecycleAction, step.App)}, nil
	case ActionRollback:
		return r.rollbackCommand(step, "<operation:"+step.ReleaseStep+">"), nil
	case ActionProbe:
		app := r.app(step.App)
		if app == nil {
			return CommandSpec{}, errors.New("probe step names an unknown app")
		}
		endpoint, err := r.approvedProbeEndpoint(step, app)
		if err != nil {
			return CommandSpec{}, err
		}
		expected, err := r.Manifest.releaseExpectations()
		if err != nil {
			return CommandSpec{}, err
		}
		return r.probeCommand(step, endpoint, expected[step.ReleaseStep].Release), nil
	case ActionInspect:
		return r.operationCommand(step.OperationID), nil
	case ActionResume:
		return r.resolveCommand(step.OperationID, operationKey(r.Manifest.RunID, step.ID+".resolve")), nil
	case ActionTeardownProof:
		return CommandSpec{}, nil
	default:
		return CommandSpec{}, errors.New("unsupported step action")
	}
}

func (r *Runner) dryRun(phase Phase, steps []Step) (RunReport, error) {
	ledger := Ledger{Schema: LedgerSchema, RunID: r.Manifest.RunID, ManifestHash: manifestHash(r.Manifest), Entries: []LedgerEntry{}, Resources: resourcesFor(r.Manifest.OwnedResources)}
	report := RunReport{Phase: phase, DryRun: true, Steps: []StepReport{}, Ledger: ledger}
	for _, step := range steps {
		command, err := r.commandFor(step, ledger)
		if err != nil {
			return report, err
		}
		outcome := formatCommand(command)
		if step.Action == ActionTeardownProof {
			outcome = "read proof " + step.Proof
		}
		report.Steps = append(report.Steps, StepReport{ID: step.ID, Action: step.Action, State: "dry-run", Outcome: outcome})
	}
	return report, nil
}

func formatCommand(command CommandSpec) string {
	return strings.Join(append([]string{command.Binary}, command.Args...), " ")
}

func (r *Runner) runStep(ctx context.Context, ledger *Ledger, step Step) (StepReport, error) {
	switch step.Action {
	case ActionPlan:
		return r.runPlan(ctx, ledger, step)
	case ActionApply:
		return r.runApply(ctx, ledger, step)
	case ActionStatus:
		return r.runStatus(ctx, ledger, step)
	case ActionLifecycle:
		return r.runLifecycle(ctx, ledger, step)
	case ActionRollback:
		return r.runRollback(ctx, ledger, step)
	case ActionProbe:
		return r.runProbe(ctx, ledger, step)
	case ActionInspect:
		return r.runInspect(ctx, ledger, step)
	case ActionResume:
		return r.runResume(ctx, ledger, step)
	case ActionTeardownProof:
		return r.runTeardownProof(ledger, step)
	default:
		return StepReport{}, errors.New("unsupported step action")
	}
}

func (r *Runner) runPlan(ctx context.Context, ledger *Ledger, step Step) (StepReport, error) {
	entry, found, err := r.prepareEntry(ledger, step, false)
	if err != nil {
		return StepReport{}, err
	}
	if found && entry.State == "succeeded" {
		return reportFor(entry), nil
	}
	if found {
		return StepReport{}, errors.New("plan entry needs inspection before replay")
	}
	command := r.planCommand(step)
	entry.CommandSHA256 = commandHash(command)
	if err := r.persistPrepared(ledger, entry, command); err != nil {
		return StepReport{}, err
	}
	response, err := r.readEnvelope(ctx, command, true)
	if err != nil {
		return r.markUnknown(ledger, entry, err)
	}
	var planned struct {
		PlanID string `json:"plan_id"`
	}
	if err := decodeData(response, &planned); err != nil || planned.PlanID == "" {
		return r.markUnknown(ledger, entry, errors.New("plan response is invalid"))
	}
	entry.State, entry.PlanID = "succeeded", planned.PlanID
	entry.UpdatedAt = time.Now().UTC()
	if err := r.replaceEntry(ledger, entry); err != nil {
		return StepReport{}, err
	}
	if err := r.Ledger.Save(*ledger); err != nil {
		return StepReport{}, err
	}
	return reportFor(entry), nil
}

func (r *Runner) runApply(ctx context.Context, ledger *Ledger, step Step) (StepReport, error) {
	planID, err := r.planReference(*ledger, step)
	if err != nil {
		return StepReport{}, err
	}
	entry, found, err := r.prepareEntry(ledger, step, true)
	if err != nil {
		return StepReport{}, err
	}
	if found && slices.Contains([]string{"succeeded", "failed", "rolled_back"}, entry.State) {
		return reportApplyOutcome(entry, step.ExpectedOutcome)
	}
	if found && entry.State != "prepared" {
		return StepReport{}, errors.New("existing apply entry requires inspection")
	}
	entry.PlanID, entry.ReleaseID = planID, step.releaseID
	entry.OperationKind = ops.Deploy
	command := r.applyCommand(planID, entry.Key)
	entry.CommandSHA256 = commandHash(command)
	if !found {
		if err := r.persistPrepared(ledger, entry, command); err != nil {
			return StepReport{}, err
		}
	}
	response, err := r.readEnvelope(ctx, command, false)
	if err != nil {
		if isKnownFailure(err) {
			entry.State, entry.UpdatedAt = "rejected", time.Now().UTC()
			if saveErr := r.replaceEntry(ledger, entry); saveErr != nil {
				return StepReport{}, saveErr
			}
			if saveErr := r.Ledger.Save(*ledger); saveErr != nil {
				return StepReport{}, saveErr
			}
			return reportFor(entry), err
		}
		return r.markUnknown(ledger, entry, err)
	}
	var accepted struct {
		Status      string `json:"status"`
		OperationID string `json:"operation_id"`
	}
	if err := decodeData(response, &accepted); err != nil || accepted.Status != "accepted" || accepted.OperationID == "" {
		return r.markUnknown(ledger, entry, errors.New("apply response is invalid"))
	}
	entry.OperationID, entry.State, entry.UpdatedAt = accepted.OperationID, "accepted", time.Now().UTC()
	if err := r.replaceEntry(ledger, entry); err != nil {
		return StepReport{}, err
	}
	if err := r.Ledger.Save(*ledger); err != nil {
		return StepReport{}, err
	}
	status, err := r.waitOperation(ctx, entry)
	if err != nil {
		return StepReport{ID: step.ID, Action: step.Action, State: "accepted", OperationID: accepted.OperationID}, err
	}
	entry.State, entry.UpdatedAt = status, time.Now().UTC()
	if err := r.replaceEntry(ledger, entry); err != nil {
		return StepReport{}, err
	}
	if err := r.Ledger.Save(*ledger); err != nil {
		return StepReport{}, err
	}
	return reportApplyOutcome(entry, step.ExpectedOutcome)
}

func reportApplyOutcome(entry LedgerEntry, expected string) (StepReport, error) {
	if expected == "" {
		expected = "succeeded"
	}
	if entry.State != expected {
		return reportFor(entry), errors.New("Brine operation did not reach the expected outcome")
	}
	return reportFor(entry), nil
}

func (r *Runner) runLifecycle(ctx context.Context, ledger *Ledger, step Step) (StepReport, error) {
	if step.App == "" || !slices.Contains([]string{"stop", "start", "restart", "remove"}, step.LifecycleAction) {
		return StepReport{}, errors.New("lifecycle step needs an exact lifecycle action")
	}
	planStep := Step{ID: step.ID + ".plan", Action: ActionPlan, App: step.App}
	planStep.Spec = step.Spec
	if step.Spec == "" {
		planStep.Action = ActionLifecycle
	}
	planID, err := r.lifecyclePlan(ctx, planStep, step.LifecycleAction)
	if err != nil {
		return StepReport{}, err
	}
	apply := Step{ID: step.ID, Action: ActionApply, App: step.App, PlanID: planID}
	return r.runApply(ctx, ledger, apply)
}

func (r *Runner) runRollback(ctx context.Context, ledger *Ledger, step Step) (StepReport, error) {
	_, sourceRelease, err := r.releaseFor(*ledger, step)
	if err != nil {
		return StepReport{}, err
	}
	entry, found, err := r.prepareEntry(ledger, step, true)
	if err != nil {
		return StepReport{}, err
	}
	if found {
		if _, _, err := r.releaseFor(*ledger, Step{App: step.App, ReleaseStep: step.ID}); err != nil {
			return StepReport{}, err
		}
		return reportApplyOutcome(entry, "succeeded")
	}
	releaseID := sourceRelease.OperationID
	command := r.rollbackCommand(step, releaseID)
	response, err := r.readEnvelope(ctx, command, true)
	if err != nil {
		return StepReport{}, err
	}
	var rollback struct {
		PlanID    string `json:"plan_id"`
		ReleaseID string `json:"release_id"`
	}
	if err := decodeData(response, &rollback); err != nil || rollback.PlanID == "" || rollback.ReleaseID != releaseID {
		return StepReport{}, errors.New("rollback selected a different release")
	}
	return r.runApply(ctx, ledger, Step{ID: step.ID, Action: ActionRollback, App: step.App, PlanID: rollback.PlanID, releaseID: rollback.ReleaseID})
}

func (r *Runner) runStatus(ctx context.Context, ledger *Ledger, step Step) (StepReport, error) {
	app := r.app(step.App)
	if app == nil {
		return StepReport{}, errors.New("status step names an unknown app")
	}
	var expected ReleaseExpectation
	var release LedgerEntry
	if step.ExpectedOutcome == "ready" || step.ExpectedOutcome == "absent" {
		var err error
		expected, release, err = r.releaseFor(*ledger, step)
		if err != nil {
			return StepReport{}, err
		}
	}
	commandApp := step.App
	if step.ExpectedOutcome == "absent" {
		commandApp = ""
	}
	response, err := r.readEnvelope(ctx, r.statusCommand(commandApp), true)
	if err != nil {
		return StepReport{}, err
	}
	var report apps.Report
	if err := decodeData(response, &report); err != nil {
		return StepReport{}, err
	}
	if step.ExpectedOutcome == "absent" {
		for _, item := range report.Apps {
			if item.App == app.Name || item.UDP != nil && item.UDP.BindAddress == app.Endpoint.BindAddress && item.UDP.HostPort == target.Port(app.Endpoint.HostPort) {
				return StepReport{}, errors.New("owned app or UDP endpoint remains in status")
			}
		}
		proof, err := loadHashedStrict[ProductionProof](step.Proof, step.ProofSHA256, maxReceipt)
		if err != nil {
			return StepReport{}, err
		}
		if err := validateProofFor(proof, "absent", app, expected, release.OperationID, r.Manifest.RunID, r.Manifest.Target.Name); err != nil {
			return StepReport{}, err
		}
		return StepReport{ID: step.ID, Action: step.Action, State: "absent"}, nil
	}
	item, err := findApp(report, step.App)
	if err != nil {
		return StepReport{}, err
	}
	if step.ExpectedOutcome == "ready" {
		if err := validateReadyFor(*item, app, expected, release.OperationID, release.PlanID, step.Proof, step.ProofSHA256, r.Manifest.RunID, r.Manifest.Target.Name); err != nil {
			return StepReport{}, err
		}
	}
	if step.ExpectedOutcome == "stopped" {
		if item.UDP == nil || item.UDP.Reservation != "active" || item.UDP.Local != "stopped" {
			return StepReport{}, errors.New("stopped status does not retain the exact UDP reservation")
		}
	}
	return StepReport{ID: step.ID, Action: step.Action, State: "observed"}, nil
}

func (r *Runner) runProbe(ctx context.Context, ledger *Ledger, step Step) (StepReport, error) {
	app := r.app(step.App)
	if app == nil || !validProbeStep(step) {
		return StepReport{}, errors.New("probe needs app and vantage")
	}
	endpoint, err := r.approvedProbeEndpoint(step, app)
	if err != nil {
		return StepReport{}, err
	}
	expected, _, err := r.releaseFor(*ledger, step)
	if err != nil {
		return StepReport{}, err
	}
	command := r.probeCommand(step, endpoint, expected.Release)
	result, err := r.runReadCommand(ctx, command, 15*time.Second)
	if err != nil && len(result.Stdout) == 0 {
		return StepReport{}, err
	}
	var probe client.Result
	decoder := json.NewDecoder(bytes.NewReader(result.Stdout))
	decoder.DisallowUnknownFields()
	if decodeErr := decoder.Decode(&probe); decodeErr != nil {
		return StepReport{}, decodeErr
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return StepReport{}, errors.New("client result has trailing data")
	}
	if err := validateProbeResult(probe, step, expected, endpoint); err != nil {
		return StepReport{}, err
	}
	return StepReport{ID: step.ID, Action: step.Action, State: probe.Outcome, Outcome: probe.Outcome}, nil
}

func (r *Runner) approvedProbeEndpoint(step Step, app *AppSpec) (string, error) {
	endpoint := step.Endpoint
	if endpoint == "" {
		endpoint = net.JoinHostPort(app.Endpoint.ClientAddress, strconv.Itoa(int(app.Endpoint.HostPort)))
	}
	if _, err := protocol.Endpoint(endpoint); err != nil {
		return "", errors.New("probe endpoint is invalid")
	}
	appEndpoint := net.JoinHostPort(app.Endpoint.ClientAddress, strconv.Itoa(int(app.Endpoint.HostPort)))
	if endpoint != appEndpoint && endpoint != r.Manifest.Target.ControlledUnreachable {
		return "", errors.New("probe endpoint is not approved")
	}
	if endpoint == appEndpoint && step.ExpectedOutcome != "ok" || endpoint == r.Manifest.Target.ControlledUnreachable && step.ExpectedOutcome != "timeout" {
		return "", errors.New("probe outcome does not match approved endpoint")
	}
	return endpoint, nil
}

func validateProbeResult(probe client.Result, step Step, expected ReleaseExpectation, endpoint string) error {
	if probe.Schema != protocol.SchemaVersion || probe.Fixture != protocol.FixtureName || probe.Endpoint != endpoint || probe.Vantage != step.Vantage || probe.Nonce != step.Nonce || probe.ExpectedRelease != expected.Release || probe.Attempts < 1 || probe.Attempts > 3 || probe.AttemptTimeoutMs != 1000 || probe.OverallTimeoutMs != 5000 || probe.StartedAt.IsZero() || probe.FinishedAt.IsZero() || probe.FinishedAt.Before(probe.StartedAt) || probe.Outcome != step.ExpectedOutcome {
		return errors.New("client evidence does not match the complete approved probe contract")
	}
	if probe.Outcome == "ok" {
		if probe.Error != "" || probe.Response == nil || probe.Response.Schema != protocol.SchemaVersion || probe.Response.Fixture != protocol.FixtureName || probe.Response.Release != expected.Release || probe.Response.Nonce != step.Nonce {
			return errors.New("successful client evidence lacks a complete echo response")
		}
		return nil
	}
	if probe.Response != nil || probe.Error == "" {
		return errors.New("timeout client evidence has an unexpected response")
	}
	return nil
}

func (r *Runner) runInspect(ctx context.Context, ledger *Ledger, step Step) (StepReport, error) {
	entry, err := r.entryForOperation(*ledger, step)
	if err != nil {
		return StepReport{}, err
	}
	status, err := r.operationStatus(ctx, entry)
	if err != nil {
		return StepReport{}, err
	}
	return StepReport{ID: step.ID, Action: step.Action, State: status, OperationID: entry.OperationID}, nil
}

func (r *Runner) runResume(ctx context.Context, ledger *Ledger, step Step) (StepReport, error) {
	entry, err := r.entryForOperation(*ledger, step)
	if err != nil {
		return StepReport{}, err
	}
	status, err := r.operationStatus(ctx, entry)
	if err != nil {
		return StepReport{}, err
	}
	if status != "recovery_required" {
		return StepReport{ID: step.ID, Action: step.Action, State: status, OperationID: entry.OperationID}, nil
	}
	resolve := Step{ID: step.ID + ".resolve", Action: ActionApply, App: entry.App, OperationID: entry.OperationID}
	return r.resolve(ctx, ledger, resolve)
}

func (r *Runner) runTeardownProof(ledger *Ledger, step Step) (StepReport, error) {
	proof, err := loadHashedStrict[TeardownProof](step.Proof, step.ProofSHA256, maxReceipt)
	if err != nil || proof.Schema != ReceiptSchema || proof.RunID != r.Manifest.RunID || proof.Target != r.Manifest.Target.Name || !proof.ForeignUntouched {
		return StepReport{}, errors.New("teardown proof is not accepted")
	}
	if err := compareTeardownResources(r.Manifest.OwnedResources, proof.Resources); err != nil {
		return StepReport{}, err
	}
	for i := range ledger.Resources {
		ledger.Resources[i].State = "absent"
	}
	if err := r.Ledger.Save(*ledger); err != nil {
		return StepReport{}, err
	}
	return StepReport{ID: step.ID, Action: step.Action, State: "absent"}, nil
}

func (r *Runner) resolve(ctx context.Context, ledger *Ledger, step Step) (StepReport, error) {
	entry, err := r.entryForOperation(*ledger, step)
	if err != nil {
		return StepReport{}, err
	}
	if entry.State == "succeeded" {
		return reportFor(entry), nil
	}
	if entry.Key == "" {
		entry.Key = operationKey(r.Manifest.RunID, step.ID)
	}
	command := r.resolveCommand(entry.OperationID, entry.Key)
	if err := r.persistPrepared(ledger, entry, command); err != nil {
		return StepReport{}, err
	}
	response, err := r.readEnvelope(ctx, command, false)
	if err != nil {
		if isKnownFailure(err) {
			entry.State, entry.UpdatedAt = "rejected", time.Now().UTC()
			if saveErr := r.replaceEntry(ledger, entry); saveErr != nil {
				return StepReport{}, saveErr
			}
			if saveErr := r.Ledger.Save(*ledger); saveErr != nil {
				return StepReport{}, saveErr
			}
			return reportFor(entry), err
		}
		return r.markUnknown(ledger, entry, err)
	}
	var accepted struct {
		Status      string `json:"status"`
		OperationID string `json:"operation_id"`
	}
	if err := decodeData(response, &accepted); err != nil || accepted.Status != "accepted" {
		return r.markUnknown(ledger, entry, errors.New("resolve response is invalid"))
	}
	if !operationIDPattern.MatchString(accepted.OperationID) {
		return reportFor(entry), errors.New("resolve response has an invalid successor operation ID")
	}
	entry.RecoveryOf = entry.OperationID
	entry.OperationKind = ops.Resolve
	entry.OperationID, entry.State = accepted.OperationID, "accepted"
	entry.UpdatedAt = time.Now().UTC()
	if err := r.replaceEntry(ledger, entry); err != nil {
		return StepReport{}, err
	}
	if err := r.Ledger.Save(*ledger); err != nil {
		return StepReport{}, err
	}
	status, err := r.waitOperation(ctx, entry)
	if err != nil {
		return StepReport{}, err
	}
	entry.State, entry.UpdatedAt = status, time.Now().UTC()
	if err := r.replaceEntry(ledger, entry); err != nil {
		return StepReport{}, err
	}
	if err := r.Ledger.Save(*ledger); err != nil {
		return StepReport{}, err
	}
	return reportFor(entry), nil
}

func (r *Runner) lifecyclePlan(ctx context.Context, step Step, action string) (string, error) {
	command := CommandSpec{Binary: r.Manifest.Source.HostBinary.Path, Args: r.baseArgs(action, step.App)}
	response, err := r.readEnvelope(ctx, command, true)
	if err != nil {
		return "", err
	}
	var plan struct {
		PlanID string `json:"plan_id"`
	}
	if err := decodeData(response, &plan); err != nil || plan.PlanID == "" {
		return "", errors.New("lifecycle plan response is invalid")
	}
	return plan.PlanID, nil
}

func (r *Runner) planReference(ledger Ledger, step Step) (string, error) {
	if (step.PlanID == "") == (step.PlanStep == "") {
		return "", errors.New("apply needs one exact plan selection")
	}
	if step.PlanID != "" {
		return step.PlanID, nil
	}
	source, err := r.Manifest.planSource(step)
	if err != nil {
		return "", err
	}
	for _, entry := range ledger.Entries {
		if entry.ID != source.ID {
			continue
		}
		if entry.Action != ActionPlan || entry.App != step.App || entry.State != "succeeded" || entry.PlanID == "" || entry.Key != operationKey(r.Manifest.RunID, source.ID) || entry.CommandSHA256 != commandHash(r.planCommand(source)) {
			return "", errors.New("source plan is not a settled ledger-owned plan")
		}
		return entry.PlanID, nil
	}
	return "", errors.New("apply plan reference is not settled")
}

func (r *Runner) entryForOperation(ledger Ledger, step Step) (LedgerEntry, error) {
	for _, entry := range ledger.Entries {
		if entry.ID == step.ID && entry.OperationID != "" {
			return entry, nil
		}
		if step.OperationID != "" && entry.OperationID == step.OperationID {
			return entry, nil
		}
	}
	return LedgerEntry{}, errors.New("operation is not owned by this ledger")
}

func (r *Runner) prepareEntry(ledger *Ledger, step Step, mutating bool) (LedgerEntry, bool, error) {
	for _, entry := range ledger.Entries {
		if entry.ID != step.ID {
			continue
		}
		if entry.Action != step.Action || entry.App != step.App {
			return LedgerEntry{}, false, errors.New("ledger step identity changed")
		}
		if mutating && entry.State == "unknown" || mutating && entry.State == "accepted" {
			return entry, true, errors.New("operation needs inspection before replay")
		}
		return entry, true, nil
	}
	return LedgerEntry{ID: step.ID, Action: step.Action, App: step.App, Key: operationKey(r.Manifest.RunID, step.ID), State: "prepared", UpdatedAt: time.Now().UTC()}, false, nil
}

func (r *Runner) persistPrepared(ledger *Ledger, entry LedgerEntry, command CommandSpec) error {
	entry.CommandSHA256 = commandHash(command)
	if err := r.replaceEntry(ledger, entry); err != nil {
		return err
	}
	return r.Ledger.Save(*ledger)
}

func (r *Runner) replaceEntry(ledger *Ledger, entry LedgerEntry) error {
	for i := range ledger.Entries {
		if ledger.Entries[i].ID == entry.ID {
			ledger.Entries[i] = entry
			return nil
		}
	}
	ledger.Entries = append(ledger.Entries, entry)
	return nil
}

func (r *Runner) markUnknown(ledger *Ledger, entry LedgerEntry, cause error) (StepReport, error) {
	entry.State = "unknown"
	entry.UpdatedAt = time.Now().UTC()
	if err := r.replaceEntry(ledger, entry); err != nil {
		return StepReport{}, err
	}
	if err := r.Ledger.Save(*ledger); err != nil {
		return StepReport{}, err
	}
	return reportFor(entry), fmt.Errorf("recovery-required for %s: %w", entry.ID, cause)
}

func (r *Runner) readEnvelope(ctx context.Context, command CommandSpec, readOnly bool) (envelope, error) {
	result, err := r.runReadCommand(ctx, command, readOnlyDeadline(r.Manifest))
	if err != nil {
		return envelope{}, err
	}
	var response envelope
	if json.Unmarshal(result.Stdout, &response) != nil || response.SchemaVersion != 1 || response.Command == "" {
		return envelope{}, errors.New("Brine envelope is invalid")
	}
	if !response.OK {
		if response.Error == nil {
			return envelope{}, errors.New("Brine returned an error without a code")
		}
		return envelope{}, &knownFailure{code: response.Error.Code}
	}
	if response.Error != nil {
		return envelope{}, errors.New("successful Brine envelope contains an error")
	}
	return response, nil
}

type envelope struct {
	SchemaVersion int             `json:"schema_version"`
	Command       string          `json:"command"`
	OK            bool            `json:"ok"`
	Data          json.RawMessage `json:"data"`
	Error         *machineError   `json:"error"`
}

type machineError struct {
	Code string `json:"code"`
}

type knownFailure struct {
	code string
}

func (e *knownFailure) Error() string { return "Brine refused operation with " + e.code }

func isKnownFailure(err error) bool {
	var known *knownFailure
	return errors.As(err, &known)
}

func decodeData(response envelope, value any) error {
	if len(response.Data) == 0 || bytes.Equal(bytes.TrimSpace(response.Data), []byte("null")) {
		return errors.New("Brine response has no data")
	}
	return json.Unmarshal(response.Data, value)
}

func (r *Runner) runReadCommand(ctx context.Context, command CommandSpec, deadline time.Duration) (CommandResult, error) {
	attempts := r.Manifest.ReadAttempts
	if attempts == 0 {
		attempts = 3
	}
	limit := r.Manifest.OutputBytes
	if limit == 0 {
		limit = maxOutput
	}
	var last CommandResult
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		last, lastErr = r.Commands.Run(ctx, command, deadline, limit)
		if lastErr == nil && last.ExitCode == 0 {
			return last, nil
		}
		if len(last.Stdout) > 0 || command.Mutating {
			break
		}
	}
	if lastErr != nil {
		return last, lastErr
	}
	return last, fmt.Errorf("command exited with status %d", last.ExitCode)
}

func readOnlyDeadline(manifest Manifest) time.Duration {
	if manifest.ReadDeadlineSeconds == 0 {
		return 30 * time.Second
	}
	return time.Duration(manifest.ReadDeadlineSeconds) * time.Second
}

func (r *Runner) waitOperation(ctx context.Context, entry LedgerEntry) (string, error) {
	for attempt := 0; attempt < r.readAttempts(); attempt++ {
		status, err := r.operationStatus(ctx, entry)
		if err != nil {
			if attempt+1 == r.readAttempts() {
				return "", err
			}
			continue
		}
		if slices.Contains([]string{"succeeded", "failed", "rolled_back", "recovery_required"}, status) {
			return status, nil
		}
	}
	return "", errors.New("operation did not settle within the read bound")
}

func (r *Runner) operationStatus(ctx context.Context, entry LedgerEntry) (string, error) {
	response, err := r.readEnvelope(ctx, r.operationCommand(entry.OperationID), true)
	if err != nil {
		return "", err
	}
	var status struct {
		Operation ops.Operation `json:"operation"`
	}
	if err := decodeData(response, &status); err != nil {
		return "", err
	}
	o := status.Operation
	if entry.OperationKind != ops.Deploy && entry.OperationKind != ops.Resolve || entry.App == "" || entry.PlanID == "" || o.ID != entry.OperationID || o.App != entry.App || o.PlanID != entry.PlanID || o.Kind != entry.OperationKind || o.RecoveryOf != entry.RecoveryOf || o.State == "" {
		return "", errors.New("operation status does not match its submitted identity")
	}
	return string(o.State), nil
}

func (r *Runner) readAttempts() int {
	if r.Manifest.ReadAttempts == 0 {
		return 3
	}
	return r.Manifest.ReadAttempts
}

func (r *Runner) baseArgs(command, app string) []string {
	args := []string{"--json", "--no-input", command}
	if app != "" {
		args = append(args, app)
	}
	args = append(args, "--target", r.Manifest.Target.Name)
	if r.Manifest.Target.ConfigDir != "" {
		args = append(args, "--config-dir", r.Manifest.Target.ConfigDir)
	}
	return args
}

func (r *Runner) planCommand(step Step) CommandSpec {
	return CommandSpec{Binary: r.Manifest.Source.HostBinary.Path, Args: append(r.baseArgs("plan", ""), step.Spec), Mutating: false}
}
func (r *Runner) applyCommand(planID, key string) CommandSpec {
	args := append(r.baseArgs("apply", ""), planID, "--idempotency-key", key)
	return CommandSpec{Binary: r.Manifest.Source.HostBinary.Path, Args: args, Mutating: true}
}
func (r *Runner) statusCommand(app string) CommandSpec {
	return CommandSpec{Binary: r.Manifest.Source.HostBinary.Path, Args: r.baseArgs("status", app)}
}
func (r *Runner) operationCommand(id string) CommandSpec {
	return CommandSpec{Binary: r.Manifest.Source.HostBinary.Path, Args: append(r.baseArgs("status", ""), "--operation", id)}
}
func (r *Runner) rollbackCommand(step Step, releaseID string) CommandSpec {
	return CommandSpec{Binary: r.Manifest.Source.HostBinary.Path, Args: append(r.baseArgs("rollback", step.App), "--release", releaseID)}
}
func (r *Runner) resolveCommand(id, key string) CommandSpec {
	return CommandSpec{Binary: r.Manifest.Source.HostBinary.Path, Args: append(r.baseArgs("resolve", ""), id, "--idempotency-key", key), Mutating: true}
}

func (r *Runner) app(name string) *AppSpec {
	for i := range r.Manifest.Apps {
		if r.Manifest.Apps[i].Name == name {
			return &r.Manifest.Apps[i]
		}
	}
	return nil
}

func findApp(report apps.Report, name string) (*apps.Status, error) {
	for i := range report.Apps {
		if report.Apps[i].App == name {
			return &report.Apps[i], nil
		}
	}
	return nil, errors.New("app is absent from Brine status")
}

func containsDomain(domains []spec.Domain, expected string) bool {
	for _, domain := range domains {
		if string(domain) == expected {
			return true
		}
	}
	return false
}

func validateReadyFor(status apps.Status, app *AppSpec, expected ReleaseExpectation, releaseID, planID, proofPath, proofHash, runID, targetName string) error {
	if app == nil || status.Current == nil || status.UDP == nil || status.Health.Direct != "healthy" || status.UDP.Local != "ready" || status.UDP.Remote != "unverified" || status.UDP.Reservation != "active" {
		return errors.New("Brine status is not production-ready")
	}
	if status.App != app.Name || status.Current.ID != releaseID || planID == "" || status.Current.PlanID != planID || status.UDP.Incarnation != app.Incarnation || status.Current.ImageDigest != expected.ImageDigest || !containsDomain(status.Current.Domains, app.Domain) || status.Current.UDP == nil || status.Current.UDP.Endpoint.BindAddress != app.Endpoint.BindAddress || status.Current.UDP.Endpoint.HostPort != target.Port(app.Endpoint.HostPort) || status.Current.UDP.ContainerPort != target.Port(app.Endpoint.ContainerPort) || status.UDP.ClientAddress != app.Endpoint.ClientAddress || string(status.UDP.Exposure) != app.Endpoint.Exposure {
		return errors.New("Brine status does not match the approved app identity")
	}
	if proofPath == "" {
		return errors.New("ready status requires a production proof receipt")
	}
	var proof ProductionProof
	var err error
	if proofHash == "" {
		proof, err = loadStrict[ProductionProof](proofPath, maxReceipt)
	} else {
		proof, err = loadHashedStrict[ProductionProof](proofPath, proofHash, maxReceipt)
	}
	if err != nil {
		return err
	}
	return validateProofFor(proof, "ready", app, expected, releaseID, runID, targetName)
}

func validateProofFor(proof ProductionProof, kind string, app *AppSpec, expected ReleaseExpectation, releaseID, runID, targetName string) error {
	if app == nil || proof.Schema != ReceiptSchema || proof.RunID == "" || proof.Target == "" || runID != "" && proof.RunID != runID || targetName != "" && proof.Target != targetName || proof.Kind != kind || proof.App != app.Name || proof.Release != expected.Release || proof.ImageDigest != expected.ImageDigest || proof.ReleaseID != releaseID || proof.Incarnation != app.Incarnation || proof.Endpoint != app.Endpoint || proof.NetworkMode != "pasta" || proof.InodeGate != "confirmed" || proof.ForeignOverlap != "none" || !proof.RouteUnchanged {
		return errors.New("production proof does not match the approved endpoint")
	}
	if kind == "ready" {
		if proof.HTTPHealth != "healthy" || !proof.Mapping.Present || proof.Mapping.BindAddress != app.Endpoint.BindAddress || proof.Mapping.HostPort != app.Endpoint.HostPort || proof.Mapping.ContainerPort != app.Endpoint.ContainerPort || proof.Socket == nil || proof.Socket.App != app.Name || proof.Socket.Address != app.Endpoint.BindAddress || proof.Socket.Port != app.Endpoint.HostPort || proof.Socket.Process == "" || proof.Socket.Unit == "" || proof.Socket.Inode == 0 || proof.Socket.Source != "ss -H -lunpe" || proof.Socket.Ownership != "affirmed" {
			return errors.New("ready proof lacks full UDP ownership evidence")
		}
		return nil
	}
	if proof.HTTPHealth != "absent" || proof.Mapping.Present || proof.Socket != nil {
		return errors.New("absence proof still contains a live publication")
	}
	return nil
}

func compareTeardownResources(expected []OwnedResource, actual []TeardownResource) error {
	if len(expected) != len(actual) {
		return errors.New("teardown proof has a different resource set")
	}
	for _, resource := range expected {
		found := false
		for _, item := range actual {
			if resource.Kind == item.Kind && resource.ID == item.ID && item.State == "absent" {
				found = true
				break
			}
		}
		if !found {
			return errors.New("owned resource remains present")
		}
	}
	return nil
}

func reportFor(entry LedgerEntry) StepReport {
	return StepReport{ID: entry.ID, Action: entry.Action, State: entry.State, PlanID: entry.PlanID, OperationID: entry.OperationID}
}

func operationKey(runID, stepID string) string {
	sum := sha256.Sum256([]byte(runID + "\x00" + stepID))
	return hex.EncodeToString(sum[:16])
}

func commandHash(command CommandSpec) string {
	raw, _ := json.Marshal(command)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func manifestHash(manifest Manifest) string {
	raw, _ := json.Marshal(manifest)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path) // #nosec G304 -- The manifest validates this pinned path before hashing.
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("file is not regular")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func readBounded(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path) // #nosec G304 -- The manifest validates this bounded receipt path before reading.
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("file exceeds bound")
	}
	return io.ReadAll(io.LimitReader(file, limit+1))
}

func loadStrict[T any](path string, limit int64) (T, error) {
	var value T
	raw, err := readBounded(path, limit)
	if err != nil {
		return value, err
	}
	return decodeStrict[T](raw)
}

func loadHashedStrict[T any](path, expectedHash string, limit int64) (T, error) {
	var value T
	if !hashPattern.MatchString(expectedHash) {
		return value, errors.New("proof hash is required")
	}
	raw, err := readBounded(path, limit)
	if err != nil {
		return value, err
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != expectedHash {
		return value, errors.New("proof hash does not match")
	}
	return decodeStrict[T](raw)
}

func decodeStrict[T any](raw []byte) (T, error) {
	var value T
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return value, errors.New("receipt has trailing data")
	}
	return value, nil
}

func ManifestHashForCLI(manifest Manifest) string { return manifestHash(manifest) }
