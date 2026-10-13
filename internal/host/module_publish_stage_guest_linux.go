//go:build linux

package host

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/spacetimedb"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

type moduleStageAction string

const (
	moduleStagePublisher moduleStageAction = "publisher_verify"
	moduleStagePreflight moduleStageAction = "preflight"
	moduleStagePublish   moduleStageAction = "publish"
	moduleStageCreate    moduleStageAction = "create"
	moduleStageObserve   moduleStageAction = "observe"
)

type moduleStageInput struct {
	Action            moduleStageAction  `json:"action"`
	Runtime           data.EngineRuntime `json:"runtime"`
	Token             string             `json:"publisher_token"`
	WasmSHA256        string             `json:"wasm_sha256"`
	ProgramWireHash   string             `json:"program_wire_hash"`
	SchemaSHA256      string             `json:"schema_sha256"`
	PreflightSHA256   string             `json:"preflight_sha256"`
	PreflightTokenSHA string             `json:"preflight_token_sha256"`
	CreationName      string             `json:"creation_name"`
	ExpectedPublisher string             `json:"expected_publisher"`
}

type moduleStageObservation struct {
	Action            moduleStageAction `json:"action"`
	CredentialSHA256  string            `json:"credential_sha256"`
	Publisher         string            `json:"publisher"`
	PublisherProof    []byte            `json:"publisher_proof"`
	Preflight         []byte            `json:"preflight"`
	Receipt           []byte            `json:"receipt"`
	ReceiptBindingSHA string            `json:"receipt_binding_sha256"`
	Database          string            `json:"database"`
	ProgramWireHash   string            `json:"program_wire_hash"`
	SchemaSHA256      string            `json:"schema_sha256"`
	PublicationKnown  bool              `json:"publication_known"`
	ChildStopped      bool              `json:"child_stopped"`
	ChildBound        bool              `json:"child_bound"`
	ChildPID          uint32            `json:"child_pid"`
	ChildStartTicks   uint64            `json:"child_start_ticks"`
}

// RunModuleStage operates only on the privately mounted copy. The enclosing
// controller proves source, roles and exact live process ownership before stdin.
func RunModuleStage(ctx context.Context, input io.Reader, output io.Writer) (stageErr error) {
	code := "guest_containment"
	defer func() {
		if stageErr != nil {
			_ = emitModuleStageDiagnostic(output, code)
		}
	}()
	if moduleStageContained() != nil {
		return datapublish.ErrRecord
	}
	code = "guest_input"
	request, err := readModuleStageInput(input)
	if err != nil {
		return err
	}
	defer func() { request.Token = "" }()
	code = "guest_source"
	root, err := os.OpenRoot("/data")
	if err != nil {
		return datapublish.ErrRecord
	}
	disabled := engineModuleHTTPDisabled(root)
	_ = root.Close()
	if !disabled {
		return datapublish.ErrRecord
	}
	code = "guest_credential"
	credential, err := spacetimedb.NewPublisherCredential([]byte(request.Token))
	if err != nil {
		return datapublish.ErrRecord
	}
	wait, cancel := context.WithTimeout(ctx, time.Duration(request.Runtime.StartupSeconds+request.Runtime.StopSeconds+35)*time.Second)
	defer cancel()
	startup, stopStartup := context.WithTimeout(wait, time.Duration(request.Runtime.StartupSeconds)*time.Second)
	defer stopStartup()
	code = "guest_child_start"
	diagnostics := &moduleNativeDiagnostics{}
	child, err := localexec.StartForeground(wait, localexec.ForegroundCommand{Path: engineNativePath, Args: engineNativeArguments(request.Runtime, privateEnginePort),
		Env: []string{"PATH=/usr/bin:/bin", "HOME=/tmp", "TMPDIR=/tmp", "TOKIO_WORKER_THREADS=2"}, Dir: "/tmp", Stderr: diagnostics})
	if err != nil {
		return datapublish.ErrRecord
	}
	exited := child.Done()
	code = "guest_child_bind"
	handle, bindErr := bindModuleStageNativeChild(startup, child, request.Runtime)
	var observation moduleStageObservation
	observeErr := bindErr
	if bindErr == nil {
		code = "guest_operation"
		defer handle.close()
		observation, observeErr = observeBoundModuleStage(wait, startup, handle, request, credential)
		code = moduleStageFailureCode(observeErr, code)
	}
	stopErr := stopEngineChild(wait, child, exited, time.Duration(request.Runtime.StopSeconds)*time.Second)
	if observeErr != nil || stopErr != nil || wait.Err() != nil || handle == nil || !handle.exited() || !engineReplayAlone(request.Runtime.PIDsLimit) {
		code = moduleStageTerminalCode(code, observeErr, stopErr)
		if observeErr == nil && stopErr != nil {
			code = moduleChildStopFailureCode(stopErr, diagnostics.panicSeen())
		}
		return datapublish.ErrRecord
	}
	observation.ChildStopped = true
	observation.ChildBound, observation.ChildPID, observation.ChildStartTicks = true, handle.pid, handle.ticks
	code = "guest_output"
	return json.NewEncoder(output).Encode(observation)
}

func observeBoundModuleStage(ctx, startup context.Context, handle *engineNativeHandle, request moduleStageInput, credential spacetimedb.PublisherCredential) (moduleStageObservation, error) {
	if err := waitModuleStageListener(startup); err != nil {
		return moduleStageObservation{}, moduleStageStepError{code: "guest_listener"}
	}
	process := "/proc/" + strconv.FormatUint(uint64(handle.pid), 10)
	if handle.alive() != nil || !moduleStageChildBindings(process, handle.pid, request.Runtime) || handle.alive() != nil || startup.Err() != nil {
		return moduleStageObservation{}, moduleStageStepError{code: "guest_child_bind"}
	}
	observed, err := executeModuleStage(ctx, request, credential)
	if err != nil {
		return moduleStageObservation{}, moduleStageStepError{code: moduleStageFailureCode(err, "guest_operation")}
	}
	return observed, nil
}

func executeModuleStage(ctx context.Context, request moduleStageInput, credential spacetimedb.PublisherCredential) (moduleStageObservation, error) {
	server, err := spacetimedb.NewPrivateServerEndpoint("127.0.0.1", privateEnginePort)
	if err != nil {
		return moduleStageObservation{}, datapublish.ErrRecord
	}
	verified, err := spacetimedb.VerifyNativePublisher(ctx, server, credential)
	if err != nil || !verified.At(server) {
		return moduleStageObservation{}, moduleStageStepError{code: "guest_publisher"}
	}
	observation := moduleStageObservation{Action: request.Action, CredentialSHA256: verified.CredentialSHA256(), Publisher: string(verified.Publisher()), PublisherProof: verified.StorageBytes()}
	if request.Action == moduleStagePublisher {
		return observation, nil
	}
	if request.Action == moduleStageCreate {
		return executeInitialModuleStage(ctx, request, server, credential, verified, observation)
	}
	if observation.Publisher != string(request.Runtime.Publisher) {
		return moduleStageObservation{}, datapublish.ErrRecord
	}
	return executeInitializedModuleStage(ctx, request, credential, observation)
}

func executeInitialModuleStage(ctx context.Context, request moduleStageInput, server spacetimedb.PrivateServerEndpoint, credential spacetimedb.PublisherCredential, verified spacetimedb.NativePublisherVerification, observation moduleStageObservation) (moduleStageObservation, error) {
	if string(verified.Publisher()) != request.ExpectedPublisher {
		return moduleStageObservation{}, moduleStageStepError{code: "guest_publisher"}
	}
	artifact, err := readModuleDescriptorArtifact()
	if err != nil || string(artifact.WasmSHA256()) != request.WasmSHA256 || string(artifact.ProgramWireHash()) != request.ProgramWireHash {
		return moduleStageObservation{}, moduleStageStepError{code: "guest_create_artifact"}
	}
	receipt, err := spacetimedb.PublishNativeCreate(ctx, server, credential, verified, request.CreationName, artifact.Bytes())
	if err != nil || !receipt.At(server) || string(receipt.Publisher()) != request.ExpectedPublisher || receipt.Name() != request.CreationName || receipt.WasmSHA256() != artifact.WasmSHA256() || receipt.ProgramWireHash() != artifact.ProgramWireHash() {
		return moduleStageObservation{}, moduleStageStepError{code: "guest_create_publish", cause: datapublish.ErrInspection}
	}
	endpoint, err := spacetimedb.NewPrivateEndpoint("127.0.0.1", privateEnginePort, string(receipt.Database()))
	if err != nil {
		return moduleStageObservation{}, moduleStageStepError{code: "guest_create_endpoint", cause: datapublish.ErrInspection}
	}
	expected, err := spacetimedb.NewExpectedDatabase(string(receipt.Database()), request.ExpectedPublisher, []string{request.CreationName})
	if err != nil {
		return moduleStageObservation{}, moduleStageStepError{code: "guest_create_expected", cause: datapublish.ErrInspection}
	}
	current, err := spacetimedb.ObserveCurrent(ctx, endpoint, credential, expected, spacetimedb.DefaultObserverLimits())
	if err != nil || string(current.CurrentModule()) != request.ProgramWireHash || current.Schema().SHA256() != request.SchemaSHA256 {
		return moduleStageObservation{}, moduleStageStepError{code: "guest_create_observe", cause: datapublish.ErrInspection}
	}
	observation.Receipt, observation.ReceiptBindingSHA, observation.PublicationKnown = receipt.ResponseBytes(), receipt.BindingSHA256(), true
	return moduleStageMetadata(observation, current), nil
}

func executeInitializedModuleStage(ctx context.Context, request moduleStageInput, credential spacetimedb.PublisherCredential, observation moduleStageObservation) (moduleStageObservation, error) {
	endpoint, err := spacetimedb.NewPrivateEndpoint("127.0.0.1", privateEnginePort, string(request.Runtime.Database))
	if err != nil {
		return moduleStageObservation{}, datapublish.ErrRecord
	}
	expected, err := spacetimedb.NewExpectedDatabase(string(request.Runtime.Database), string(request.Runtime.Publisher), request.Runtime.Names)
	if err != nil {
		return moduleStageObservation{}, datapublish.ErrRecord
	}
	current, err := spacetimedb.ObserveCurrent(ctx, endpoint, credential, expected, spacetimedb.DefaultObserverLimits())
	if err != nil {
		return moduleStageObservation{}, datapublish.ErrRecord
	}
	if request.Action == moduleStageObserve {
		return moduleStageMetadata(observation, current), nil
	}
	if !nativeObservationMatches(request.Runtime, current) {
		return moduleStageObservation{}, datapublish.ErrRecord
	}
	artifact, err := readModuleDescriptorArtifact()
	if err != nil || string(artifact.WasmSHA256()) != request.WasmSHA256 || string(artifact.ProgramWireHash()) != request.ProgramWireHash {
		return moduleStageObservation{}, datapublish.ErrRecord
	}
	preflight, err := spacetimedb.PreflightNativePublication(ctx, endpoint, credential, artifact.Bytes())
	if err != nil || !preflight.Automatic() || preflight.MajorUpgrade() {
		return moduleStageObservation{}, datapublish.ErrRecord
	}
	observation.Preflight = preflight.ResponseBytes()
	if request.Action == moduleStagePreflight {
		return moduleStageMetadata(observation, current), nil
	}
	if preflight.ResponseSHA256() != request.PreflightSHA256 || preflight.TokenSHA256() != request.PreflightTokenSHA {
		return moduleStageObservation{}, datapublish.ErrRecord
	}
	receipt, publishErr := spacetimedb.PublishNativeUpdate(ctx, endpoint, credential, artifact.Bytes(), preflight)
	if publishErr == nil && receipt.At(endpoint) && receipt.WasmSHA256() == artifact.WasmSHA256() && receipt.ProgramWireHash() == artifact.ProgramWireHash() {
		observation.Receipt, observation.ReceiptBindingSHA, observation.PublicationKnown = receipt.ResponseBytes(), receipt.BindingSHA256(), true
	}
	current, err = spacetimedb.ObserveCurrent(ctx, endpoint, credential, expected, spacetimedb.DefaultObserverLimits())
	if err != nil {
		return moduleStageObservation{}, datapublish.ErrRecord
	}
	return moduleStageMetadata(observation, current), nil
}

func moduleStageMetadata(observation moduleStageObservation, metadata spacetimedb.Observation) moduleStageObservation {
	observation.Database, observation.Publisher = string(metadata.Database()), string(metadata.Publisher())
	observation.ProgramWireHash, observation.SchemaSHA256 = string(metadata.CurrentModule()), metadata.Schema().SHA256()
	return observation
}

func waitModuleStageListener(ctx context.Context) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		connection, err := (&net.Dialer{Timeout: 100 * time.Millisecond}).DialContext(ctx, "tcp", "127.0.0.1:23991")
		if err == nil {
			_ = connection.Close()
			if ctx.Err() != nil {
				return datapublish.ErrRecord
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return datapublish.ErrRecord
		case <-ticker.C:
		}
	}
}

func readModuleStageInput(input io.Reader) (moduleStageInput, error) {
	raw, err := io.ReadAll(io.LimitReader(input, (96<<10)+1))
	if err != nil || len(raw) == 0 || len(raw) > 96<<10 {
		return moduleStageInput{}, datapublish.ErrRecord
	}
	defer clear(raw)
	if _, err = strictjson.Object(raw, "action", "runtime", "publisher_token", "wasm_sha256", "program_wire_hash", "schema_sha256", "preflight_sha256", "preflight_token_sha256", "creation_name", "expected_publisher"); err != nil {
		return moduleStageInput{}, datapublish.ErrRecord
	}
	var request moduleStageInput
	if json.Unmarshal(raw, &request) != nil || request.Runtime.Validate() != nil || len(request.Token) == 0 || len(request.Token) > 16<<10 || !request.Runtime.SecretsAdmit(time.Now().UTC(), time.Duration(request.Runtime.StartupSeconds+request.Runtime.StopSeconds+35)*time.Second) {
		return moduleStageInput{}, datapublish.ErrRecord
	}
	if request.Action == moduleStagePublisher {
		if request.Runtime.State != data.EngineUnpublished || request.WasmSHA256 != "" || request.ProgramWireHash != "" || request.SchemaSHA256 != "" || request.PreflightSHA256 != "" || request.PreflightTokenSHA != "" || request.CreationName != "" || request.ExpectedPublisher != "" {
			return moduleStageInput{}, datapublish.ErrRecord
		}
		return request, nil
	}
	if request.Action == moduleStageCreate {
		if request.Runtime.State != data.EngineUnpublished || !data.ValidCatalogHash(request.ExpectedPublisher) || len(request.CreationName) == 0 || len(request.CreationName) > 63 || !data.ValidCatalogHash(request.WasmSHA256) || !data.ValidCatalogHash(request.ProgramWireHash) || !data.ValidCatalogHash(request.SchemaSHA256) || request.PreflightSHA256 != "" || request.PreflightTokenSHA != "" {
			return moduleStageInput{}, datapublish.ErrRecord
		}
		return request, nil
	}
	if request.Runtime.State != data.EngineInitialized || request.Action != moduleStagePreflight && request.Action != moduleStagePublish && request.Action != moduleStageObserve || !data.ValidCatalogHash(request.WasmSHA256) || !data.ValidCatalogHash(request.ProgramWireHash) || !data.ValidCatalogHash(request.SchemaSHA256) || request.CreationName != "" || request.ExpectedPublisher != "" {
		return moduleStageInput{}, datapublish.ErrRecord
	}
	if request.Action == moduleStagePublish && (!data.ValidCatalogHash(request.PreflightSHA256) || !data.ValidCatalogHash(request.PreflightTokenSHA)) {
		return moduleStageInput{}, datapublish.ErrRecord
	}
	return request, nil
}

func moduleStageContained() error {
	if os.Getpid() != 1 || os.Geteuid() != 1000 || os.Getegid() != 1000 {
		return datapublish.ErrRecord
	}
	interfaces, err := net.Interfaces()
	if err != nil || len(interfaces) != 1 || interfaces[0].Flags&net.FlagLoopback == 0 {
		return datapublish.ErrRecord
	}
	for _, path := range []string{"/work", "/home/brine", "/run/podman/podman.sock", "/var/run/docker.sock"} {
		if _, err = os.Lstat(path); !os.IsNotExist(err) {
			return datapublish.ErrRecord
		}
	}
	raw, err := readModuleGuestMountInfo()
	if err != nil || !moduleStageMounts(raw) {
		return datapublish.ErrRecord
	}
	status, err := engineProcessStatus("/proc/self")
	if err != nil || !engineProcessOwned(status) || !slices.Equal(strings.Fields(status["NSpid"]), []string{"1"}) {
		return datapublish.ErrRecord
	}
	return nil
}

func moduleStageMounts(raw []byte) bool {
	if len(raw) == 0 || len(raw) > 64<<10 {
		return false
	}
	required := map[string]string{"/": "ro", "/data": "rw", "/keys/private.pem": "ro", "/keys/public.pem": "ro", "/brine": "ro", "/proc": "rw", "/sys": "ro", "/sys/fs/cgroup": "ro", "/tmp": "rw"}
	optional := map[string]bool{moduleDescriptorArtifactPath: true}
	for _, path := range []string{"/dev", "/dev/pts", "/dev/shm", "/dev/mqueue", "/dev/null", "/dev/zero", "/dev/full", "/dev/tty", "/dev/random", "/dev/urandom", "/run", "/var/tmp", "/run/.containerenv", "/etc/hostname", "/etc/hosts", "/etc/resolv.conf", "/proc/keys", "/proc/latency_stats", "/proc/timer_list", "/proc/asound", "/proc/bus", "/proc/fs", "/proc/irq", "/proc/sys", "/proc/sysrq-trigger", "/sys/firmware"} {
		optional[path] = true
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		before, _, found := strings.Cut(line, " - ")
		fields := strings.Fields(before)
		if !found || len(fields) < 6 || seen[fields[4]] || !moduleStageMountMode(required, optional, fields[4], fields[5]) {
			return false
		}
		seen[fields[4]] = true
	}
	for path := range required {
		if !seen[path] {
			return false
		}
	}
	return true
}

func moduleStageMountMode(required map[string]string, optional map[string]bool, path, flags string) bool {
	if path == moduleDescriptorArtifactPath {
		return moduleDescriptorMountMode(map[string]string{path: "ro"}, nil, path, flags)
	}
	return moduleDescriptorMountMode(required, optional, path, flags)
}
