package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/backupcredentials"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datainit"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/transport"
)

var deploymentStepNames = []string{"preparation", "credentials", "initialization", "release"}

type firstDeployHost struct {
	databases                          []string
	credentialStates                   map[string]bool
	initializedStates                  map[string]bool
	credentialPlans                    map[string]backupcredentials.Receipt
	initPlans                          map[string]string
	terminalFailure                    bool
	blockInitObservation               bool
	stateBlocked                       bool
	checkpointPlans                    map[string]dispatch.Planned
	changeAfterInit                    bool
	reviewChanged                      bool
	recoveryFailure                    bool
	stateless                          bool
	released                           bool
	t                                  *testing.T
	prepared, credentials, initialized bool
	failAt, failPoll, loseAcceptance   string
	failed                             bool
	submitted                          []string
	accepted                           map[string]jobs.Status
	counts                             map[string]int
	credential                         backupcredentials.Receipt
}

func (h *firstDeployHost) Call(ctx context.Context, _ transport.Target, r dispatch.Request) (result.Envelope, error) {
	success := func(value any) (result.Envelope, error) { return result.Success("brine host "+r.Op, value), nil }
	hash := "sha256:" + strings.Repeat("a", 64)
	reviewHash := hash
	if h.reviewChanged {
		reviewHash = "sha256:" + strings.Repeat("9", 64)
	}
	switch r.Op {
	case "deploy_checkpoint":
		var q dispatch.DeploymentCheckpointArgs
		if json.Unmarshal(r.Args, &q) != nil {
			h.t.Fatal("invalid checkpoint query")
		}
		checked := dispatch.DeploymentCheckpoint{PlanID: q.PlanID, ReviewHash: reviewHash}
		switch q.Step {
		case "preparation", "release":
			p, ok := h.checkpointPlans[q.PlanID]
			if !ok {
				return result.Envelope{}, result.New(result.Conflict, nil)
			}
			checked.Planned = &p
		case "credentials":
			if _, ok := h.credentialPlans[q.PlanID]; !ok {
				return result.Envelope{}, result.New(result.Conflict, nil)
			}
		case "initialization":
			if db, ok := h.initPlans[q.PlanID]; !ok || db != q.Database {
				return result.Envelope{}, result.New(result.Conflict, nil)
			}
		}
		return success(checked)
	case "deploy_operation":
		var q dispatch.DeploymentOperationQuery
		if json.Unmarshal(r.Args, &q) != nil {
			h.t.Fatal("invalid lookup")
		}
		for id, status := range h.accepted {
			op := status.Operation
			if op.Kind == q.Kind && (op.Kind == ops.Deploy && op.PlanID == q.PlanID || op.Kind.IsTask() && op.SecretRef == q.PlanID) {
				return success(dispatch.DeploymentOperation{OperationID: id})
			}
		}
		return success(dispatch.DeploymentOperation{})
	case "deploy_state":
		if h.stateBlocked {
			return result.Envelope{}, result.New(result.Conflict, nil)
		}
		if h.stateless {
			return success(dispatch.DeploymentState{ReviewHash: reviewHash, Databases: []dispatch.DeploymentDatabase{}})
		}
		names := h.databases
		if len(names) == 0 {
			names = []string{"main"}
		}
		databases := []dispatch.DeploymentDatabase{}
		for _, name := range names {
			credentials, initialized := h.credentials, h.initialized
			if len(names) > 1 {
				credentials = h.credentialStates[name]
				initialized = h.initializedStates[name]
			}
			databases = append(databases, dispatch.DeploymentDatabase{Name: name, Prepared: h.prepared, Credentials: credentials, Initialized: initialized})
		}
		return success(dispatch.DeploymentState{ReviewHash: reviewHash, Databases: databases})
	case "data_prepare_plan":
		p := dispatch.Planned{PlanID: hash, Kind: plan.Create, Conflicts: []plan.Diagnostic{}}
		h.checkpointPlans[p.PlanID] = p
		return success(p)
	case "plan":
		kind := plan.Create
		if h.released {
			kind = plan.NoOp
		}
		id := "sha256:" + strings.Repeat("b", 64)
		if h.reviewChanged {
			id = "sha256:" + strings.Repeat("8", 64)
			kind = plan.Update
		}
		p := dispatch.Planned{PlanID: id, Kind: kind, Conflicts: []plan.Diagnostic{}}
		h.checkpointPlans[p.PlanID] = p
		return success(p)
	case "backup_credentials_plan":
		receipt := h.credential
		if len(h.databases) > 1 {
			var args dispatch.BackupCredentialPlanArgs
			if json.Unmarshal(r.Args, &args) != nil {
				h.t.Fatal("credential plan args")
			}
			binding := sha256.Sum256([]byte(args.Database))
			receipt.Scope.Binding = hex.EncodeToString(binding[:16])
			p := backupcredentials.Plan{Requester: receipt.Requester, Kind: backupcredentials.Kind, Scope: receipt.Scope, Version: receipt.Version}
			raw, err := json.Marshal(p)
			if err != nil {
				h.t.Fatal(err)
			}
			sum := sha256.Sum256(raw)
			receipt.PlanID = "sha256:" + hex.EncodeToString(sum[:])
		}
		h.credentialPlans[receipt.PlanID] = receipt
		return success(backupcredentials.Plan{Requester: receipt.Requester, ID: receipt.PlanID, Kind: backupcredentials.Kind, Scope: receipt.Scope, Version: receipt.Version})
	case "data_init_plan":
		var request dispatch.DataInitPlanArgs
		if json.Unmarshal(r.Args, &request) != nil {
			h.t.Fatal("invalid init request")
		}
		if request.FirstReleasePlan != hash {
			h.t.Fatal("initialization lost preparation plan")
		}
		initializer, err := datainit.DecodeArtifact(request.ArtifactBytes, request.Artifact)
		if err != nil {
			h.t.Fatal(err)
		}
		catalog, err := data.InitializerCatalog(ctx, initializer)
		if err != nil {
			h.t.Fatal(err)
		}
		requester, err := datainit.ParseRequester("deploy:" + strings.Repeat("a", 64))
		if err != nil {
			h.t.Fatal(err)
		}
		p := datainit.Plan{Request: datainit.Request(request), Requester: requester, Catalog: catalog, ReplicaEpoch: data.ReplicaEpochID(strings.Repeat("b", 32)), RemotePrefix: "fixture/epoch", RestorePointID: strings.Repeat("c", 32), Bounds: data.InitializationBounds{MaxBackupAgeSeconds: 300, MaxRestoreTestAgeSeconds: 300, RecoveryWindowSeconds: 3600}, PolicyHash: hash, TargetHash: hash, DesiredHash: hash, Database: data.DatabaseBinding{Name: initializer.Definition.Database, DatabaseID: data.DatabaseID(strings.Repeat("d", 32)), IncarnationID: data.AppIncarnationID(strings.Repeat("e", 32)), ReplicaBindingID: data.ReplicaBindingID(strings.Repeat("f", 32))}, Definition: initializer.Definition, Source: data.AllocatedEmpty}
		raw, err := json.Marshal(struct {
			Plan      datainit.Plan      `json:"plan"`
			Requester datainit.Requester `json:"requester"`
		}{p, requester})
		if err != nil {
			h.t.Fatal(err)
		}
		sum := sha256.Sum256(raw)
		p.ID = "sha256:" + hex.EncodeToString(sum[:])
		if !p.Valid() {
			h.t.Fatal("invalid init fixture")
		}
		h.initPlans[p.ID] = string(initializer.Definition.Database)
		return success(p)
	case "operation":
		var args dispatch.OperationArgs
		if json.Unmarshal(r.Args, &args) != nil {
			h.t.Fatal("bad status args")
		}
		status, ok := h.accepted[args.OperationID]
		if !ok {
			h.t.Fatal("unknown job", args.OperationID)
		}
		if h.failPoll == args.OperationID && !h.failed {
			h.failed = true
			return result.Envelope{}, result.New(result.Interrupted, nil)
		}
		if status.Operation.Kind == ops.DataInitApply {
			h.stateBlocked = false
			if h.changeAfterInit {
				h.reviewChanged = true
				h.released = true
			}
		}
		return success(status)
	case "apply", "backup_credentials_set", "data_init_apply":
		name := ""
		reference := ""
		kind := ops.Deploy
		database := ""
		var credentialReceipt backupcredentials.Receipt
		switch r.Op {
		case "apply":
			var args dispatch.ApplyArgs
			if json.Unmarshal(r.Args, &args) != nil {
				h.t.Fatal("bad apply")
			}
			reference = args.PlanID
			if reference == hash {
				name = "preparation"
			} else {
				name = "release"
			}
		case "backup_credentials_set":
			name = "credentials"
			kind = ops.CredentialActivation
			var args struct {
				Database string `json:"database"`
				PlanID   string `json:"plan_id"`
			}
			if json.Unmarshal(r.Args, &args) != nil {
				h.t.Fatal("credential set args")
			}
			database = args.Database
			reference = args.PlanID
			credentialReceipt = h.credentialPlans[reference]
			if !strings.Contains(string(r.Args), "PLANTED_SECRET") {
				h.t.Fatal("missing credential packet")
			}
		case "data_init_apply":
			name = "initialization"
			kind = ops.DataInitApply
			var args dispatch.DataInitApplyArgs
			if json.Unmarshal(r.Args, &args) != nil {
				h.t.Fatal("bad init apply")
			}
			reference = args.PlanID
			database = h.initPlans[reference]
		}
		failed := h.failAt == name && !h.failed
		if failed {
			h.failed = true
			if !h.terminalFailure {
				return result.Envelope{}, result.New(result.PolicyRefused, nil)
			}
		}
		stepName := name
		if len(h.databases) > 1 && database != "" {
			stepName = name + ":" + database
		}
		h.submitted = append(h.submitted, stepName)
		h.counts[stepName]++
		id := name + "-op"
		if len(h.databases) > 1 && database != "" {
			id = name + "-" + database + "-op"
		}
		if h.counts[name] > 1 {
			id = fmt.Sprintf("%s-op-%d", name, h.counts[name])
		}
		operation := ops.Operation{ID: id, Kind: kind, App: "hello", PlanID: reference, State: ops.Succeeded}
		status := jobs.Status{Operation: operation, Events: []ops.Event{}}
		if kind.IsTask() {
			status.Operation.PlanID = ""
			status.Operation.SecretRef = reference
			var receipt any
			if name == "credentials" {
				receipt = credentialReceipt
				if !failed {
					h.credentials = true
					h.credentialStates[database] = true
				}
			} else {
				receipt = datainit.Operation{ID: strings.Repeat("1", 32), PlanID: reference, State: "succeeded", Fence: data.FenceID(strings.Repeat("2", 32))}
				if !failed {
					h.initialized = true
					h.initializedStates[database] = true
				}
			}
			raw, err := json.Marshal(receipt)
			if err != nil {
				h.t.Fatal(err)
			}
			status.Outcome = &ops.TaskOutcome{Receipt: raw}
		}
		if name == "preparation" && !failed {
			h.prepared = true
		}
		if name == "release" && !failed {
			h.released = true
		}
		if failed {
			status.Operation.State = ops.Failed
			if kind.IsTask() {
				status.Outcome = &ops.TaskOutcome{Error: result.PolicyRefused}
			}
			if h.recoveryFailure {
				status.Operation.State = ops.RecoveryRequired
				if kind.IsTask() {
					status.Outcome = &ops.TaskOutcome{Error: result.RecoveryRequired}
				}
			}
		}
		h.accepted[id] = status
		if name == "initialization" && h.blockInitObservation {
			h.stateBlocked = true
		}
		if h.loseAcceptance == name && !h.failed {
			h.failed = true
			return result.Envelope{}, result.New(result.TransportInvalidResponse, nil)
		}
		return success(jobs.Accepted{Status: "accepted", OperationID: id})
	default:
		h.t.Fatal("unexpected call", r.Op)
	}
	return result.Envelope{}, fmt.Errorf("unreachable fixture operation")
}
func deploymentFixture(t *testing.T) (*firstDeployHost, Dependencies, []string, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	raw, err := os.ReadFile("../../fixtures/sqlite-app/brine.toml")
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.ReplaceAll(raw, []byte("sqlite-fixture"), []byte("hello"))
	if err = os.WriteFile(filepath.Join(dir, "brine.toml"), raw, 0600); err != nil { // #nosec G703 -- Test-owned directory and a fixed fixture filename.
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "schema.sql"), []byte("CREATE TABLE t(x TEXT);\n"), 0600); err != nil {
		t.Fatal(err)
	}
	credentialFile := filepath.Join(dir, "credentials.json")
	if err = os.WriteFile(credentialFile, []byte(`{"access_key_id":"PLANTED_KEY","secret_access_key":"PLANTED_SECRET"}`), 0600); err != nil {
		t.Fatal(err)
	}
	out, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	deps := testDependencies(t, out, stderr)
	host := &firstDeployHost{checkpointPlans: map[string]dispatch.Planned{}, t: t, accepted: map[string]jobs.Status{}, counts: map[string]int{}, credential: backupCredentialReceipt(t), credentialStates: map[string]bool{}, initializedStates: map[string]bool{}, credentialPlans: map[string]backupcredentials.Receipt{}, initPlans: map[string]string{}}
	deps.LoadOperationTarget = func(string, string) (transport.Target, error) { return transport.Target{Name: "fixture"}, nil }
	deps.OperationClient = host
	args := []string{"deploy", filepath.Join(dir, "brine.toml"), "--target", "fixture", "--config-dir", filepath.Join(dir, "targets"), "--backup-credentials", credentialFile, "--yes", "--json"}
	return host, deps, args, out, stderr
}
func TestDeployNewAppOrderedJobsAndMachineIDs(t *testing.T) {
	host, deps, args, out, stderr := deploymentFixture(t)
	if err := Execute(deps, args); err != nil {
		t.Fatal(err, stderr.String())
	}
	if strings.Join(host.submitted, ",") != "preparation,credentials,initialization,release" {
		t.Fatal(host.submitted)
	}
	var envelope struct {
		OK   bool             `json:"ok"`
		Data deploymentReport `json:"data"`
	}
	if json.Unmarshal(out.Bytes(), &envelope) != nil || !envelope.OK || len(envelope.Data.Steps) != 4 {
		t.Fatal(out.String())
	}
	for _, step := range envelope.Data.Steps {
		if !step.Complete || step.OperationID == "" {
			t.Fatal(step)
		}
	}
	if strings.Contains(out.String()+stderr.String(), "PLANTED") {
		t.Fatal("credential leaked")
	}
	out.Reset()
	stderr.Reset()
	if err := Execute(deps, args); err != nil {
		t.Fatal("idempotent rerun", err)
	}
	for name, count := range host.counts {
		if count != 1 {
			t.Fatal("replayed", name, count)
		}
	}
}
func TestDeployFailureAtEachStepResumesWithoutReplayingEarlierSteps(t *testing.T) {
	for i, name := range deploymentStepNames {
		t.Run(name, func(t *testing.T) {
			host, deps, args, out, stderr := deploymentFixture(t)
			host.failAt = name
			if err := Execute(deps, args); err == nil {
				t.Fatal("failure ignored")
			}
			if len(host.submitted) != i {
				t.Fatal("work passed failure", host.submitted)
			}
			if !json.Valid(out.Bytes()) {
				t.Fatal("failure has no machine report")
			}
			out.Reset()
			stderr.Reset()
			if err := Execute(deps, args); err != nil {
				t.Fatal("resume failed", err, stderr.String())
			}
			if strings.Join(host.submitted, ",") != "preparation,credentials,initialization,release" {
				t.Fatal("earlier step replayed", host.submitted)
			}
		})
	}
}
func TestDeployLostObserverResumesSameAcceptedJobs(t *testing.T) {
	for _, name := range deploymentStepNames {
		t.Run(name, func(t *testing.T) {
			host, deps, args, out, stderr := deploymentFixture(t)
			host.failPoll = name + "-op"
			if err := Execute(deps, args); err == nil {
				t.Fatal("observer failure ignored")
			}
			out.Reset()
			stderr.Reset()
			if err := Execute(deps, args); err != nil {
				t.Fatal("observer resume", err, stderr.String())
			}
			for name, count := range host.counts {
				if count != 1 {
					t.Fatal("duplicated accepted work", name, count)
				}
			}
		})
	}
}
func TestDeployInitializedOnlyPlansAndAppliesRelease(t *testing.T) {
	host, deps, args, _, stderr := deploymentFixture(t)
	host.prepared = true
	host.credentials = true
	host.initialized = true
	if err := Execute(deps, args); err != nil {
		t.Fatal(err, stderr.String())
	}
	if strings.Join(host.submitted, ",") != "release" {
		t.Fatal("initialized data modified", host.submitted)
	}
}
func TestDeployReviewAndCredentialAdmissionHaveNoEffects(t *testing.T) {
	for _, test := range []string{"review", "missing", "mode", "symlink"} {
		t.Run(test, func(t *testing.T) {
			host, deps, args, out, stderr := deploymentFixture(t)
			switch test {
			case "review":
				args = args[:len(args)-2]
				args = append(args, "--json")
			case "missing":
				args = append(args[:6], args[8:]...)
			case "mode":
				if err := os.Chmod(args[7], 0644); err != nil { // #nosec G302 -- Deliberately insecure packet must be refused.
					t.Fatal(err)
				}
			case "symlink":
				original := args[7]
				args[7] += ".link"
				if err := os.Symlink(original, args[7]); err != nil {
					t.Fatal(err)
				}
			}
			err := Execute(deps, args)
			if test == "review" && err != nil || test != "review" && err == nil {
				t.Fatal(test, err, out.String())
			}
			if len(host.submitted) != 0 {
				t.Fatal("unapproved work ran")
			}
			if test == "review" && !strings.Contains(out.String(), "CREATE TABLE t") {
				t.Fatal("review omitted SQL")
			}
			if test == "missing" && !strings.Contains(stderr.String(), "--backup-credentials credentials.json") {
				t.Fatal("missing exact guidance", stderr.String())
			}
		})
	}
}

func TestDeploySettledFailureAtEachStepAdmitsFreshReviewedAttempt(t *testing.T) {
	for _, name := range deploymentStepNames {
		t.Run(name, func(t *testing.T) {
			host, deps, args, out, stderr := deploymentFixture(t)
			host.failAt = name
			host.terminalFailure = true
			if err := Execute(deps, args); err == nil {
				t.Fatal("terminal failure ignored")
			}
			out.Reset()
			stderr.Reset()
			if err := Execute(deps, args); err != nil {
				t.Fatal("settled resume", err, stderr.String())
			}
			for previous, count := range host.counts {
				want := 1
				if previous == name {
					want = 2
				}
				if count != want {
					t.Fatal("wrong resume", previous, count)
				}
			}
		})
	}
}
func TestDeployRecoveryRequiredNeverResubmitsInitialization(t *testing.T) {
	host, deps, args, out, stderr := deploymentFixture(t)
	host.failAt = "initialization"
	host.terminalFailure = true
	host.recoveryFailure = true
	if err := Execute(deps, args); result.Classify(err).Code() != result.RecoveryRequired {
		t.Fatal(err)
	}
	out.Reset()
	stderr.Reset()
	if err := Execute(deps, args); result.Classify(err).Code() != result.RecoveryRequired {
		t.Fatal(err)
	}
	if host.counts["initialization"] != 1 || host.counts["release"] != 0 {
		t.Fatal("replayed uncertain work", host.counts)
	}
}

func TestDeploySeveralDatabasesHaveIndependentCredentialAndInitJobs(t *testing.T) {
	host, deps, args, out, stderr := deploymentFixture(t)
	host.databases = []string{"audit", "main"}
	specPath := args[1]
	raw, err := os.ReadFile(specPath) // #nosec G304 -- The path comes from this test's private fixture directory.
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, []byte(`
[[databases]]
name="audit"
persistent_root="/srv/brine-data"
mount_path="/audit"
filename="audit.db"
backup_destination="primary"
schema="audit.sql"
`)...)
	if err = os.WriteFile(specPath, raw, 0600); err != nil { // #nosec G703 -- Rewrite only the test-owned fixture spec.
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(filepath.Dir(specPath), "audit.sql"), []byte("CREATE TABLE audit(event TEXT);\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = Execute(deps, args); err != nil {
		t.Fatal(err, stderr.String())
	}
	if strings.Join(host.submitted, ",") != "preparation,credentials:audit,credentials:main,initialization:audit,initialization:main,release" {
		t.Fatal(host.submitted)
	}
	var envelope struct {
		Data deploymentReport `json:"data"`
	}
	if json.Unmarshal(out.Bytes(), &envelope) != nil || len(envelope.Data.Steps) != 6 {
		t.Fatal(out.String())
	}
	for _, step := range envelope.Data.Steps {
		if !step.Complete || step.OperationID == "" {
			t.Fatal(step)
		}
	}
}

func TestDeployStatelessKeepsOrdinaryReleasePath(t *testing.T) {
	host, deps, args, _, stderr := deploymentFixture(t)
	host.stateless = true
	raw, err := os.ReadFile("../spec/testdata/valid-minimal.toml")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(args[1], raw, 0600); err != nil {
		t.Fatal(err)
	} // #nosec G703 -- Test-owned fixture path, never external input.
	if err = Execute(deps, args); err != nil {
		t.Fatal(err, stderr.String())
	}
	if strings.Join(host.submitted, ",") != "release" {
		t.Fatal("stateless data effects", host.submitted)
	}
}

func TestDeployCompletedWorkflowDoesNotSkipLaterReleaseChanges(t *testing.T) {
	host, deps, args, out, stderr := deploymentFixture(t)
	if err := Execute(deps, args); err != nil {
		t.Fatal(err)
	}
	host.released = false // Another independently observed release now differs.
	out.Reset()
	stderr.Reset()
	if err := Execute(deps, args); err != nil {
		t.Fatal(err, stderr.String())
	}
	if host.counts["release"] != 2 || host.counts["initialization"] != 1 {
		t.Fatal("cached completion skipped fresh release", host.counts)
	}
}

func TestDeployLostAcceptanceRecoversEveryJobWithoutSecretRedelivery(t *testing.T) {
	for _, name := range deploymentStepNames {
		t.Run(name, func(t *testing.T) {
			host, deps, args, out, stderr := deploymentFixture(t)
			host.loseAcceptance = name
			if err := Execute(deps, args); err == nil {
				t.Fatal("lost response ignored")
			}
			out.Reset()
			stderr.Reset()
			if name != "preparation" {
				for i, arg := range args {
					if arg == "--backup-credentials" {
						args = append(args[:i:i], args[i+2:]...)
						break
					}
				}
			}
			if err := Execute(deps, args); err != nil {
				t.Fatal("resume", err, stderr.String())
			}
			var envelope struct {
				OK   bool             `json:"ok"`
				Data deploymentReport `json:"data"`
			}
			if json.Unmarshal(out.Bytes(), &envelope) != nil || !envelope.OK {
				t.Fatal(out.String())
			}
			for _, step := range envelope.Data.Steps {
				if !step.Complete || step.OperationID == "" {
					t.Fatal("lost accepted ID", step)
				}
			}
			for stage, count := range host.counts {
				if count != 1 {
					t.Fatal("duplicate submission", stage, count)
				}
			}
		})
	}
}

func TestDeploymentApprovalStopsAtNewlineAndCancelsBorrowedInput(t *testing.T) {
	input, output, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.Close() }()
	defer func() { _ = output.Close() }()
	if _, err = output.Write([]byte("yes\n")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err = readDeploymentApproval(ctx, input); err != nil {
		t.Fatal("waited for EOF", err)
	}
	canceled, stop := context.WithCancel(t.Context())
	stop()
	if err = readDeploymentApproval(canceled, input); err == nil {
		t.Fatal("ignored cancellation")
	}
	reader := &borrowedBlockingInput{}
	if err = readDeploymentApproval(t.Context(), reader); err == nil || reader.read {
		t.Fatal("borrowed blocking input consumed", err)
	}
}
func TestDeployNonterminalCannotPromptOrApply(t *testing.T) {
	host, deps, args, _, stderr := deploymentFixture(t)
	args = args[:len(args)-2]
	if err := Execute(deps, args); result.ExitCode(err) != 2 {
		t.Fatal(err)
	}
	if len(host.submitted) != 0 || strings.Contains(stderr.String(), "Type yes") {
		t.Fatal("prompt bypassed guard", stderr.String(), host.submitted)
	}
}

func TestDeployLostInitializationAcceptancePollsBeforeUnknownLiveSchema(t *testing.T) {
	host, deps, args, out, stderr := deploymentFixture(t)
	host.loseAcceptance = "initialization"
	host.blockInitObservation = true
	if err := Execute(deps, args); err == nil {
		t.Fatal("expected lost acceptance")
	}
	if !host.stateBlocked {
		t.Fatal("fixture did not expose unknown state")
	}
	out.Reset()
	stderr.Reset()
	if err := Execute(deps, args); err != nil {
		t.Fatal("queried readiness before observing task", err, stderr.String())
	}
	if host.counts["initialization"] != 1 {
		t.Fatal("replayed uncertain init")
	}
}

func TestDeploySubstitutedPreparationCheckpointRefusesBeforeApply(t *testing.T) {
	host, deps, args, out, stderr := deploymentFixture(t)
	host.failAt = "preparation"
	if err := Execute(deps, args); err == nil {
		t.Fatal("expected paused preparation")
	}
	files, err := filepath.Glob(filepath.Join(args[5], "deploys", "*", "journal.json"))
	if err != nil || len(files) != 1 {
		t.Fatal(files, err)
	}
	raw, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	} // #nosec G304 -- Private test-owned checkpoint.
	var journal deploymentJournal
	if err = json.Unmarshal(raw, &journal); err != nil {
		t.Fatal(err)
	}
	journal.Steps[0].PlanID = "sha256:" + strings.Repeat("f", 64)
	raw, err = json.Marshal(journal)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(files[0], raw, 0600); err != nil {
		t.Fatal(err)
	} // #nosec G703 -- Private test-owned checkpoint.
	out.Reset()
	stderr.Reset()
	if err = Execute(deps, args); err == nil {
		t.Fatal("foreign retained plan accepted")
	}
	if len(host.submitted) != 0 {
		t.Fatal("checkpoint substituted an apply", host.submitted)
	}
}
func TestDeployReleaseChangeAfterInitializationRequiresNewReview(t *testing.T) {
	host, deps, args, out, stderr := deploymentFixture(t)
	host.changeAfterInit = true
	if err := Execute(deps, args); err == nil {
		t.Fatal("unreviewed update applied")
	}
	if host.counts["release"] != 0 || strings.Join(host.submitted, ",") != "preparation,credentials,initialization" {
		t.Fatal(host.submitted)
	}
	if !strings.Contains(stderr.String(), "changed after approval") {
		t.Fatal(stderr.String())
	}
	var envelope struct {
		OK   bool             `json:"ok"`
		Data deploymentReport `json:"data"`
	}
	if json.Unmarshal(out.Bytes(), &envelope) != nil || envelope.OK || envelope.Data.Review.Release == nil || envelope.Data.Review.Release.Kind != plan.Update {
		t.Fatal("new release decision missing", out.String())
	}
	host.changeAfterInit = false
	out.Reset()
	stderr.Reset()
	if err := Execute(deps, args); err != nil {
		t.Fatal("fresh review cannot proceed", err, stderr.String())
	}
	if host.counts["initialization"] != 1 || host.counts["release"] != 1 {
		t.Fatal("replayed data", host.counts)
	}
}

type deploymentApprovalTTY struct{ terminalReader }

func (r deploymentApprovalTTY) ReadContext(ctx context.Context, p []byte) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return r.Read(p)
}
func TestDeployChangedReleaseAsksHumanAgain(t *testing.T) {
	for _, answer := range []string{"yes\n", "no\n"} {
		t.Run(strings.TrimSpace(answer), func(t *testing.T) {
			host, deps, args, _, stderr := deploymentFixture(t)
			host.changeAfterInit = true
			deps.Stdin = strings.NewReader("yes\n" + answer)
			withTestTerminal(t, &deps)
			deps.Stdin = deploymentApprovalTTY{deps.Stdin.(terminalReader)}
			args = args[:len(args)-2]
			err := Execute(deps, args)
			if answer == "yes\n" {
				if err != nil || host.counts["release"] != 1 {
					t.Fatal("new approval did not release", err, stderr.String())
				}
			} else if err == nil || host.counts["release"] != 0 {
				t.Fatal("rejected release applied", err)
			}
			if !strings.Contains(stderr.String(), "approve the changed release diff") {
				t.Fatal("second approval not requested", stderr.String())
			}
		})
	}
}

func TestDeployRetainedOperationFromAnotherAppRefuses(t *testing.T) {
	host, deps, args, _, stderr := deploymentFixture(t)
	host.failPoll = "preparation-op"
	if err := Execute(deps, args); err == nil {
		t.Fatal("fixture did not pause after acceptance")
	}
	operation := host.accepted["preparation-op"]
	operation.Operation.App = "another-app"
	host.accepted["preparation-op"] = operation
	submitted := len(host.submitted)
	if err := Execute(deps, args); err == nil {
		t.Fatal("foreign operation was trusted", stderr.String())
	}
	if len(host.submitted) != submitted {
		t.Fatal("work was submitted after reference mismatch", host.submitted)
	}
}
