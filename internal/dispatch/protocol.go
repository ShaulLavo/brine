// Package dispatch implements the restricted, single-request host protocol.
package dispatch

import (
	"context"
	"encoding/json"
	"io"
	"regexp"

	"github.com/ShaulLavo/brine/internal/result"
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
	"ping":      {ReadOnly, func(raw json.RawMessage) (any, error) { _, err := strictjson.Object(raw); return PingArgs{}, err }},
	"inventory": {ReadOnly, func(raw json.RawMessage) (any, error) { _, err := strictjson.Object(raw); return InventoryArgs{}, err }},
	"apply":     {Mutating, decodeApply},
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

type Server struct {
	version   string
	inventory Inventory
	jobs      JobOperations
	authorize Authorization
}

func NewServer(version string, inventory Inventory) *Server {
	return &Server{version: version, inventory: inventory}
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
	class := operations[request.Op].class
	if s.authorize == nil {
		if class != ReadOnly {
			return fail(result.New(result.DispatchOperationRefused, nil))
		}
	} else if err := s.authorize(ctx, class); err != nil {
		return fail(result.Classify(err))
	}
	args, _ := operations[request.Op].decode(request.Args)
	var value any
	switch args.(type) {
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
		accepted, err := s.jobs.Apply(ctx, args.(ApplyArgs).PlanID, args.(ApplyArgs).IdempotencyKey)
		if err != nil {
			return fail(result.Classify(err))
		}
		value = accepted
	case OperationArgs:
		if s.jobs == nil {
			return fail(result.New(result.DependencyMissing, nil))
		}
		status, err := s.jobs.Operation(ctx, args.(OperationArgs).OperationID, args.(OperationArgs).AfterCursor)
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
