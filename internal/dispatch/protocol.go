// Package dispatch implements the restricted, single-request host protocol.
package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"time"

	"github.com/ShaulLavo/brine/internal/datainit"
	"github.com/ShaulLavo/brine/internal/diagnose"
	"github.com/ShaulLavo/brine/internal/liverestore"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/logs"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/strictjson"
	"github.com/ShaulLavo/brine/internal/target"
)

const SchemaVersion = 1
const RequestLimit = 2 << 20
const ResponseLimit = 4 << 20

type Request struct {
	SchemaVersion int             `json:"schema_version"`
	Op            string          `json:"op"`
	RequestID     string          `json:"request_id"`
	Args          json.RawMessage `json:"args"`
}

type Class string

const (
	ReadOnly Class = "read_only"
	Mutating Class = "mutating"
)

type PingArgs struct{}
type InventoryArgs struct{}
type PingData struct {
	ServerVersion    string `json:"server_version"`
	ProtocolVersions []int  `json:"protocol_versions"`
}

// Inventory supplies the shared snapshot contract without transport dependencies.
type Inventory interface {
	Collect(context.Context) (target.Snapshot, error)
}

type operation struct {
	class  Class
	decode func(json.RawMessage) (any, error)
}

var operations = map[string]operation{
	"archive_maintenance_plan":              {Mutating, decodeArchiveMaintenancePlan},
	"archive_maintenance_apply":             {Mutating, decodeArchiveMaintenanceApply},
	"archive_maintenance_resolve":           {Mutating, decodeArchiveMaintenanceResolve},
	"archive_status":                        {ReadOnly, decodeArchiveMaintenanceStatus},
	"archive_credentials_plan":              {Mutating, decodeArchiveCredentialPlan},
	"archive_credentials_set":               {Mutating, decodeArchiveCredentialSet},
	"module_publish_plan":                   {Mutating, decodeModulePlan},
	"module_publish_apply":                  {Mutating, decodeModuleApply},
	"module_publish_inspect":                {ReadOnly, decodeModuleInspect},
	"module_publish_resolve":                {Mutating, decodeModuleResolve},
	"deploy_checkpoint":                     {ReadOnly, decodeDeploymentCheckpointArgs},
	"native_point":                          {ReadOnly, decodeNativePoint},
	"native_backup_inspect":                 {ReadOnly, decodeNativeBackupInspect},
	"native_backup_review":                  {Mutating, decodeNativeBackupReview},
	"native_backup_resolve":                 {Mutating, decodeNativeBackupResolve},
	"native_backup_status":                  {ReadOnly, decodeNativeBackupStatus},
	"native_backup_policy_plan":             {Mutating, decodeNativeBackupPolicyPlan},
	"native_backup_activate":                {Mutating, decodeNativeBackupPolicyActivate},
	"native_material_set":                   {Mutating, decodeNativeMaterial},
	"native_material_status":                {ReadOnly, decodeNativeMaterialInspect},
	"native_recovery_set":                   {Mutating, decodeNativeRecovery},
	"native_capture":                        {Mutating, decodeNativeCapture},
	"native_prepare":                        {Mutating, decodeNativePrepare},
	"native_prepare_abandon":                {Mutating, decodeNativeAbandon},
	"native_restore_test":                   {ReadOnly, decodeNativeRestoreTest},
	"deploy_operation":                      {ReadOnly, decodeDeploymentOperationQuery},
	"deploy_state":                          {ReadOnly, decodeDeployment},
	"data_init_plan":                        {Mutating, decodeDataInitPlan},
	"data_init_apply":                       {Mutating, decodeDataInitApply},
	"restore_test":                          {ReadOnly, decodeRestoreTest},
	"backup_credentials_plan":               {Mutating, decodeBackupCredentialPlan},
	"backup_credentials_set":                {Mutating, decodeBackupCredentialSet},
	"config_set":                            {Mutating, decodeConfig},
	"lifecycle":                             {Mutating, decodeLifecycle},
	"secret_set":                            {Mutating, decodeSecret},
	"reconcile":                             {Mutating, decodeReconcile},
	"resolve":                               {Mutating, decodeResolve},
	"diagnose":                              {ReadOnly, func(raw json.RawMessage) (any, error) { return diagnose.DecodeRequest(raw) }},
	"status":                                {ReadOnly, decodeAppStatus},
	"rollback":                              {Mutating, decodeRollback},
	"logs":                                  {ReadOnly, func(raw json.RawMessage) (any, error) { return logs.DecodeRequest(raw) }},
	"ping":                                  {ReadOnly, func(raw json.RawMessage) (any, error) { _, err := strictjson.Object(raw); return PingArgs{}, err }},
	"inventory":                             {ReadOnly, func(raw json.RawMessage) (any, error) { _, err := strictjson.Object(raw); return InventoryArgs{}, err }},
	"apply":                                 {Mutating, decodeApply},
	"plan":                                  {Mutating, decodePlan},
	"data_prepare_plan":                     {Mutating, decodePreparation},
	"operation":                             {ReadOnly, decodeOperation},
	"native_retire_plan":                    {Mutating, decodeNativeRetirePlan},
	"native_retire":                         {Mutating, decodeNativeRetireApply},
	"native_retire_inspect":                 {ReadOnly, decodeNativeRetireInspect},
	"native_retire_resolve":                 {Mutating, decodeNativeRetireResolve},
	"native_live_restore_review_resolution": {Mutating, decodeNativeRestoreResolutionReview},
	"native_live_restore_resolve":           {Mutating, decodeNativeRestoreResolve},
	"native_live_restore_plan":              {Mutating, decodeNativeLiveRestorePlan},
	"native_live_restore":                   {Mutating, decodeNativeLiveRestoreApply},
	"native_live_restore_inspect":           {ReadOnly, decodeNativeLiveRestoreInspect},
}

func ClassOf(op string) (Class, bool) { entry, ok := operations[op]; return entry.class, ok }

var requestID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func DecodeRequest(data []byte) (Request, error) {
	var request Request
	invalid := func() (Request, error) { return Request{}, result.New(result.DispatchInvalidRequest, nil) }
	if len(data) > RequestLimit {
		return invalid()
	}
	fields, err := strictjson.Object(data, "schema_version", "op", "request_id", "args")
	if err != nil {
		return invalid()
	}
	request.SchemaVersion, err = strictjson.Value[int](fields["schema_version"])
	if err != nil {
		return invalid()
	}
	if request.SchemaVersion != SchemaVersion {
		return Request{}, result.New(result.DispatchUnsupportedSchema, nil)
	}
	request.Op, err = strictjson.Value[string](fields["op"])
	if err != nil {
		return invalid()
	}
	request.RequestID, err = strictjson.Value[string](fields["request_id"])
	if err != nil || !requestID.MatchString(request.RequestID) {
		return invalid()
	}
	entry, ok := operations[request.Op]
	if !ok {
		return Request{}, result.New(result.DispatchOperationRefused, nil)
	}
	decoded, err := entry.decode(fields["args"])
	if err != nil {
		return invalid()
	}
	if private, ok := decoded.(interface{ Clear() }); ok {
		private.Clear()
	}
	request.Args = append(json.RawMessage(nil), fields["args"]...)
	return request, nil
}

func EncodeRequest(request Request) ([]byte, error) {
	data, err := json.Marshal(request)
	if err != nil {
		return nil, result.New(result.DispatchInvalidRequest, err)
	}
	if len(data)+1 > RequestLimit {
		return nil, result.New(result.DispatchInvalidRequest, nil)
	}
	checked, err := DecodeRequest(data)
	if err != nil {
		clear(data)
		return nil, err
	}
	clear(checked.Args)
	return append(data, '\n'), nil
}

type LogReader interface {
	Read(context.Context, logs.Request) ([]logs.Line, error)
}

type DiagnosticReader interface {
	Read(context.Context, diagnose.Request) (diagnose.Report, error)
}

// previewRequestKey is set only after strict request/argument validation.
type previewRequestKey struct{}

func IsReconcilePreview(ctx context.Context) bool {
	dry, _ := ctx.Value(previewRequestKey{}).(bool)
	return dry
}

type Factory func(context.Context, string) (*Server, error)

type Server struct {
	ModulePublications     ModulePublicationOperations
	DataInitialization     DataInitializationOperations
	BackupCredentials      BackupCredentialOperations
	Config                 ConfigurationOperations
	Secrets                SecretOperations
	Reconciler             ReconcileOperations
	Factory                Factory
	Planner                Planner
	Diagnose               DiagnosticReader
	Apps                   AppOperations
	Logs                   LogReader
	version                string
	inventory              Inventory
	jobs                   JobOperations
	authorize              Authorization
	RestoreTests           RestoreTestOperations
	NativePoints           NativePointInspector
	NativeBackupStatus     NativeBackupStatusOperations
	NativeBackupPolicy     NativeBackupPolicyOperations
	NativeRecovery         NativeRecoveryOperations
	NativeMaterial         NativeMaterialOperations
	NativeBackups          NativeBackupOperations
	NativeLiveRestore      NativeLiveRestoreOperations
	NativeRetire           NativeRetireOperations
	NativeBackupResolution NativeBackupResolutionOperations
	ArchiveMaintenance     ArchiveMaintenanceOperations
}

func NewServer(version string, inventory Inventory) *Server {
	return &Server{version: version, inventory: inventory, Logs: logs.Reader{Inventory: inventory, Executor: localexec.ExecRunner{}}}
}

func (s *Server) Handle(ctx context.Context, stdin io.Reader) (result.Envelope, error) {
	started := time.Now()
	command := "brine host serve"
	fail := func(err error) (result.Envelope, error) { return result.Failure(command, err), err }
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	data, err := readRequest(ctx, stdin)
	defer clear(data)
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return fail(err)
		}
		return fail(result.New(result.DispatchInvalidRequest, err))
	}
	request, err := DecodeRequest(data)
	if err != nil {
		return fail(err)
	}
	defer clear(request.Args)
	command = "brine host " + request.Op
	deadline := started.Add(RequestTimeout)
	if OperationTimeout(request.Op) != RequestTimeout {
		deadline = time.Now().Add(OperationTimeout(request.Op))
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	args, _ := operations[request.Op].decode(request.Args)
	if private, ok := args.(interface{ Clear() }); ok {
		defer private.Clear()
	}
	if preview, ok := args.(ReconcileArgs); ok && preview.DryRun {
		ctx = context.WithValue(ctx, previewRequestKey{}, true)
	}
	if s.Factory != nil {
		configured, err := s.Factory(ctx, request.Op)
		if err != nil {
			return fail(result.Classify(err))
		}
		if configured != nil {
			s = configured
		}
	}
	class := operations[request.Op].class
	if reconcile, ok := args.(ReconcileArgs); ok && reconcile.DryRun {
		class = ReadOnly
	}
	if s.authorize == nil {
		if class != ReadOnly {
			return fail(result.New(result.DispatchOperationRefused, nil))
		}
	} else if err := s.authorize(ctx, class); err != nil {
		return fail(result.Classify(err))
	}
	var value any
	switch args := args.(type) {
	case ArchiveMaintenancePlanArgs, ArchiveMaintenanceApplyArgs, ArchiveMaintenanceResolveArgs, ArchiveMaintenanceStatusArgs, ArchiveCredentialPlanArgs, ArchiveCredentialSetArgs:
		checked, err := s.archiveMaintenance(ctx, args)
		if err != nil {
			return fail(archiveMaintenanceError(err))
		}
		value = checked
	case ModulePlanArgs, ModuleApplyArgs, ModuleInspectArgs, ModuleResolveArgs:
		publication, err := s.plannedNativeDataOperation(ctx, args)
		if err != nil {
			return fail(result.Classify(err))
		}
		value = publication
	case NativeBackupInspectArgs, NativeBackupReviewArgs, NativeBackupResolveArgs:
		next, err := s.nativeBackupResolution(ctx, args)
		if err != nil {
			return fail(result.Classify(err))
		}
		value = next
	case NativeBackupStatusArgs:
		if s.NativeBackupStatus == nil {
			return fail(result.New(result.DependencyMissing, nil))
		}
		status, err := s.NativeBackupStatus.BackupStatus(ctx, args)
		if err != nil {
			return fail(result.Classify(err))
		}
		value = status
	case NativeBackupPolicyPlanArgs:
		if s.NativeBackupPolicy == nil {
			return fail(result.New(result.DependencyMissing, nil))
		}
		view, err := s.NativeBackupPolicy.PlanPolicy(ctx, args)
		if err != nil {
			return fail(result.Classify(err))
		}
		value = view
	case NativeBackupPolicyActivateArgs:
		if s.NativeBackupPolicy == nil {
			return fail(result.New(result.DependencyMissing, nil))
		}
		accepted, err := s.NativeBackupPolicy.ActivatePolicy(ctx, args)
		if err != nil {
			return fail(result.Classify(err))
		}
		value = accepted
	case NativeRetirePlanArgs, NativeRetireApplyArgs, NativeRetireInspectArgs, NativeRetireResolveArgs:
		value, err = s.plannedNativeDataOperation(ctx, args)
		if err != nil {
			return fail(result.Classify(err))
		}
	case liverestore.PlanRequest, liverestore.ApplyRequest, liverestore.InspectRequest, liverestore.ResolveRequest, NativeRestoreResolutionReviewArgs:
		value, err = s.nativeLiveRestore(ctx, args)
		if err != nil {
			return fail(result.Classify(err))
		}
	case NativeMaterialArgs:
		if s.NativeMaterial == nil {
			return fail(result.New(result.DependencyMissing, nil))
		}
		receipt, err := s.NativeMaterial.DeliverMaterial(ctx, args)
		if err != nil {
			return fail(result.Classify(err))
		}
		value = receipt
	case NativeMaterialInspectArgs:
		if s.NativeMaterial == nil {
			return fail(result.New(result.DependencyMissing, nil))
		}
		receipt, err := s.NativeMaterial.InspectMaterial(ctx, args)
		if err != nil {
			return fail(result.Classify(err))
		}
		value = receipt
	case NativeRecoveryArgs:
		defer args.Clear()
		if s.NativeRecovery == nil {
			return fail(result.New(result.DependencyMissing, nil))
		}
		accepted, err := s.NativeRecovery.SetRecovery(ctx, args)
		if err != nil {
			return fail(result.Classify(err))
		}
		value = accepted
	case NativeCaptureArgs, NativePrepareArgs, NativeRestoreTestArgs, NativeAbandonArgs:
		accepted, err := s.submitNativeBackup(ctx, args)
		if err != nil {
			return fail(result.Classify(err))
		}
		value = accepted
	case NativePointArgs:
		if s.NativePoints == nil {
			return fail(result.New(result.DependencyMissing, nil))
		}
		point, err := s.NativePoints.InspectNativePoint(ctx, args)
		if err != nil {
			return fail(result.Classify(err))
		}
		value = point
	case DataInitPlanArgs:
		if s.DataInitialization == nil {
			return fail(result.New(result.DependencyMissing, nil))
		}
		p, err := s.DataInitialization.Plan(ctx, datainit.Request(args))
		if err != nil {
			return fail(DataInitializationFailure(err))
		}
		value = p
	case DataInitApplyArgs:
		if s.DataInitialization == nil {
			return fail(result.New(result.DependencyMissing, nil))
		}
		p, err := s.DataInitialization.Apply(ctx, args.App, args.PlanID)
		if err != nil {
			return fail(DataInitializationFailure(err))
		}
		value = p
	case RestoreTestArgs:
		if s.RestoreTests == nil {
			return fail(result.New(result.DependencyMissing, nil))
		}
		receipt, err := s.RestoreTests.Test(ctx, args)
		if err != nil {
			return fail(result.Classify(err))
		}
		value = receipt
	case BackupCredentialPlanArgs:
		if s.BackupCredentials == nil {
			return fail(result.New(result.DependencyMissing, nil))
		}
		p, err := s.BackupCredentials.Plan(ctx, args.App, args.Database, args.ExpiresAt)
		if err != nil {
			return fail(credentialFailure(err))
		}
		value = p
	case BackupCredentialSetArgs:
		defer args.Packet.Clear()
		if s.BackupCredentials == nil {
			return fail(result.New(result.DependencyMissing, nil))
		}
		receipt, err := s.BackupCredentials.Set(ctx, args.App, args.Database, args.PlanID, args.Packet)
		if err != nil {
			return fail(credentialFailure(err))
		}
		value = receipt
	case ConfigArgs:
		if s.Config == nil {
			return fail(result.New(result.DependencyMissing, nil))
		}
		p, err := s.Config.ConfigSet(ctx, args.App, args.Edits)
		if err != nil {
			return fail(result.Classify(err))
		}
		value = p
	case LifecycleArgs:
		if s.Config == nil {
			return fail(result.New(result.DependencyMissing, nil))
		}
		p, err := s.Config.Lifecycle(ctx, args.App, args.Action)
		if err != nil {
			return fail(result.Classify(err))
		}
		value = p
	case SecretArgs:
		defer clear(args.Value)
		if s.Secrets == nil {
			return fail(result.New(result.DependencyMissing, nil))
		}
		stored, err := s.Secrets.Set(ctx, args.App, args.Reference, request.RequestID, args.Value)
		if err != nil {
			return fail(result.Classify(err))
		}
		value = stored

	case ResolveArgs:
		resolver, ok := s.jobs.(ResolutionJobs)
		if !ok {
			return fail(result.New(result.DependencyMissing, nil))
		}
		accepted, err := resolver.Resolve(ctx, args.OperationID, args.IdempotencyKey)
		if err != nil {
			return fail(result.Classify(err))
		}
		value = accepted
	case ReconcileArgs:
		if args.DryRun {
			if s.Reconciler == nil {
				return fail(result.New(result.DependencyMissing, nil))
			}
			report, err := s.Reconciler.DryRun(ctx)
			if err != nil {
				return fail(result.Classify(err))
			}
			value = report
		} else {
			jobs, ok := s.jobs.(ReconcileJobs)
			if !ok {
				return fail(result.New(result.DependencyMissing, nil))
			}
			accepted, err := jobs.Reconcile(ctx)
			if err != nil {
				return fail(result.Classify(err))
			}
			value = accepted
		}
	case deploymentCheckpointArgs:
		reader, ok := s.Planner.(DeploymentCheckpointReader)
		if !ok {
			return fail(result.New(result.DependencyMissing, nil))
		}
		checked, err := reader.DeploymentCheckpoint(ctx, args.App, args.Request)
		if err != nil {
			return fail(result.Classify(err))
		}
		value = checked
	case DeploymentOperationQuery:
		reader, ok := s.Planner.(DeploymentOperationReader)
		if !ok {
			return fail(result.New(result.DependencyMissing, nil))
		}
		recovered, err := reader.DeploymentOperation(ctx, args)
		if err != nil {
			return fail(result.Classify(err))
		}
		value = recovered
	case deploymentArgs:
		reader, ok := s.Planner.(DeploymentReader)
		if !ok {
			return fail(result.New(result.DependencyMissing, nil))
		}
		state, err := reader.DeploymentState(ctx, args.App)
		if err != nil {
			return fail(result.Classify(err))
		}
		value = state
	case preparationArgs:
		planner, ok := s.Planner.(PreparationPlanner)
		if !ok {
			return fail(result.New(result.DependencyMissing, nil))
		}
		prepared, err := planner.PlanDataPreparation(ctx, args.App)
		if err != nil {
			return fail(result.Classify(err))
		}
		value = prepared
	case spec.App:
		if s.Planner == nil {
			return fail(result.New(result.DependencyMissing, nil))
		}
		p, err := s.Planner.Plan(ctx, args)
		if err != nil {
			return fail(result.Classify(err))
		}
		value = p
	case diagnose.Request:
		reader := s.Diagnose
		if reader == nil {
			reader = diagnose.Reader{Inventory: s.inventory, Runner: localexec.ExecRunner{}}
		}
		report, err := reader.Read(ctx, args)
		if err != nil {
			return fail(result.Classify(err))
		}
		value = report
	case AppStatusArgs:
		if s.Apps == nil {
			return fail(result.New(result.DependencyMissing, nil))
		}
		report, err := s.Apps.Status(ctx, args.App)
		if err != nil {
			return fail(result.Classify(err))
		}
		value = report
	case RollbackArgs:
		if s.Apps == nil {
			return fail(result.New(result.DependencyMissing, nil))
		}
		planned, err := s.Apps.Rollback(ctx, args.App, args.ReleaseID)
		if err != nil {
			return fail(result.Classify(err))
		}
		value = planned
	case logs.Request:
		if s.Logs == nil {
			return fail(result.New(result.DependencyMissing, nil))
		}
		lines, err := s.Logs.Read(ctx, args)
		if err != nil {
			return fail(result.Classify(err))
		}
		if ctx.Err() != nil {
			return fail(ctx.Err())
		}
		value = lines
	case PingArgs:
		value = PingData{s.version, []int{SchemaVersion}}
	case InventoryArgs:
		if s.inventory == nil {
			return fail(result.New(result.DependencyMissing, nil))
		}
		snapshot, err := s.inventory.Collect(ctx)
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		if err != nil {
			return fail(result.Classify(err))
		}
		encoded, err := target.Encode(snapshot)
		if err != nil {
			return fail(result.New(result.InternalError, err))
		}
		value = json.RawMessage(encoded)
	case ApplyArgs:
		if s.jobs == nil {
			return fail(result.New(result.DependencyMissing, nil))
		}
		accepted, err := s.jobs.Apply(ctx, args.PlanID, args.IdempotencyKey)
		if err != nil {
			return fail(result.Classify(err))
		}
		value = accepted
	case OperationArgs:
		if s.jobs == nil {
			return fail(result.New(result.DependencyMissing, nil))
		}
		status, err := s.jobs.Operation(ctx, args.OperationID, args.AfterCursor)
		if err != nil {
			return fail(result.Classify(err))
		}
		value = status
	default:
		return fail(result.New(result.DispatchOperationRefused, nil))
	}
	response := result.Success(command, value)
	encoded, err := json.Marshal(response)
	if err != nil || len(encoded)+1 > ResponseLimit {
		return fail(result.New(result.InternalError, err))
	}
	return response, nil
}

func readRequest(parent context.Context, stdin io.Reader) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, RequestTimeout)
	defer cancel()
	if closer, ok := stdin.(io.Closer); ok {
		stopClose := context.AfterFunc(ctx, func() { _ = closer.Close() })
		defer stopClose()
	}
	data, err := io.ReadAll(io.LimitReader(stdin, RequestLimit+1))
	if ctx.Err() != nil {
		clear(data)
		return nil, ctx.Err()
	}
	return data, err
}
