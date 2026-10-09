// Package dispatch implements the restricted, single-request host protocol.
package dispatch

import (
	"context"
	"encoding/json"
	"io"
	"regexp"

	"github.com/ShaulLavo/brine/internal/diagnose"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/logs"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/strictjson"
	"github.com/ShaulLavo/brine/internal/target"
)

const SchemaVersion = 1
const RequestLimit = 64 << 10
const ResponseLimit = 1 << 20

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
	"reconcile": {Mutating, decodeReconcile},
	"diagnose":  {ReadOnly, func(raw json.RawMessage) (any, error) { return diagnose.DecodeRequest(raw) }},
	"status":    {ReadOnly, decodeAppStatus},
	"rollback":  {Mutating, decodeRollback},
	"logs":      {ReadOnly, func(raw json.RawMessage) (any, error) { return logs.DecodeRequest(raw) }},
	"ping":      {ReadOnly, func(raw json.RawMessage) (any, error) { _, err := strictjson.Object(raw); return PingArgs{}, err }},
	"inventory": {ReadOnly, func(raw json.RawMessage) (any, error) { _, err := strictjson.Object(raw); return InventoryArgs{}, err }},
	"apply":     {Mutating, decodeApply},
	"plan":      {Mutating, decodePlan},
	"operation": {ReadOnly, decodeOperation},
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
	if _, err := entry.decode(fields["args"]); err != nil {
		return invalid()
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
	if _, err := DecodeRequest(data); err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

type LogReader interface {
	Read(context.Context, logs.Request) ([]logs.Line, error)
}

type DiagnosticReader interface {
	Read(context.Context, diagnose.Request) (diagnose.Report, error)
}

type Factory func(context.Context, string) (*Server, error)

type Server struct {
	Reconciler ReconcileOperations
	Factory    Factory
	Planner    Planner
	Diagnose   DiagnosticReader
	Apps       AppOperations
	Logs       LogReader
	version    string
	inventory  Inventory
	jobs       JobOperations
	authorize  Authorization
}

func NewServer(version string, inventory Inventory) *Server {
	return &Server{version: version, inventory: inventory, Logs: logs.Reader{Inventory: inventory, Executor: localexec.ExecRunner{}}}
}

func (s *Server) Handle(ctx context.Context, stdin io.Reader) (result.Envelope, error) {
	command := "brine host serve"
	fail := func(err error) (result.Envelope, error) { return result.Failure(command, err), err }
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	data, err := io.ReadAll(io.LimitReader(stdin, RequestLimit+1))
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if err != nil {
		return fail(result.New(result.DispatchInvalidRequest, err))
	}
	request, err := DecodeRequest(data)
	if err != nil {
		return fail(err)
	}
	command = "brine host " + request.Op
	if err := ctx.Err(); err != nil {
		return fail(err)
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
	args, _ := operations[request.Op].decode(request.Args)
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
	case ReconcileArgs:
		if s.Reconciler == nil {
			return fail(result.New(result.DependencyMissing, nil))
		}
		var report any
		var err error
		if args.DryRun {
			report, err = s.Reconciler.DryRun(ctx)
		} else {
			report, err = s.Reconciler.Reconcile(ctx)
		}
		if err != nil {
			return fail(result.Classify(err))
		}
		value = report
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
