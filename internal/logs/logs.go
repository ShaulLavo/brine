// Package logs reads bounded, best-effort redacted container and unit logs for owned apps.
package logs

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/strictjson"
	"github.com/ShaulLavo/brine/internal/systemd"
	"github.com/ShaulLavo/brine/internal/target"
)

const MaxTail = 1000
const MaxBytes = 128 << 10
const ReadTimeout = 10 * time.Second
const ContainerLogDriver = "k8s-file"
const ContainerLogMaxBytes = 10 << 20

type Request struct {
	App   string `json:"app"`
	Tail  int    `json:"tail"`
	Since string `json:"since"`
}
type Line struct {
	Timestamp string `json:"timestamp"`
	Priority  int    `json:"priority"`
	Message   string `json:"message"`
}
type Inventory interface {
	Collect(context.Context) (target.Snapshot, error)
}
type Reader struct {
	Inventory Inventory
	Executor  localexec.Executor
}

type JournalReader Reader

type source uint8

const (
	containerSource source = iota
	journalSource
)

var sincePattern = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]{1,9})?(?:Z|[+-](?:[01][0-9]|2[0-3]):[0-5][0-9])$`)

var appName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

func (r Request) Validate() error {
	if !appName.MatchString(r.App) || r.Tail < 1 || r.Tail > MaxTail {
		return result.New(result.InvalidUsage, nil)
	}
	if r.Since != "" {
		if !sincePattern.MatchString(r.Since) {
			return result.New(result.InvalidUsage, nil)
		}
		if _, err := time.Parse(time.RFC3339, r.Since); err != nil {
			return result.New(result.InvalidUsage, nil)
		}
	}
	return nil
}
func DecodeRequest(raw []byte) (Request, error) {
	fields, err := strictjson.Object(raw, "app", "tail", "since")
	if err != nil {
		fields, err = strictjson.Object(raw, "app", "tail")
		if err != nil {
			return Request{}, err
		}
	}
	var r Request
	r.App, err = strictjson.Value[string](fields["app"])
	if err != nil {
		return r, err
	}
	r.Tail, err = strictjson.Value[int](fields["tail"])
	if err != nil {
		return r, err
	}
	if raw, ok := fields["since"]; ok {
		r.Since, err = strictjson.Value[string](raw)
		if err != nil {
			return r, err
		}
	}
	return r, r.Validate()
}
func (r Reader) Read(ctx context.Context, request Request) ([]Line, error) {
	return r.read(ctx, request, containerSource)
}

func (r JournalReader) Read(ctx context.Context, request Request) ([]Line, error) {
	return Reader(r).read(ctx, request, journalSource)
}

func (r Reader) read(ctx context.Context, request Request, from source) ([]Line, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, collectionError(err, result.LogsInventoryFailed, result.LogsInventoryTimeout)
	}
	if r.Inventory == nil || r.Executor == nil {
		return nil, result.New(result.DependencyMissing, nil)
	}
	ctx, cancel := context.WithTimeout(ctx, ReadTimeout)
	defer cancel()
	snapshot, err := r.Inventory.Collect(ctx)
	if err != nil {
		return nil, collectionError(err, result.LogsInventoryFailed, result.LogsInventoryTimeout)
	}
	if err := ctx.Err(); err != nil {
		return nil, collectionError(err, result.LogsInventoryFailed, result.LogsInventoryTimeout)
	}
	unit, err := ownedUnit(snapshot, request.App)
	if err != nil {
		return nil, err
	}
	if from == containerSource {
		return r.readContainer(ctx, request, unit)
	}
	args := []string{"--user", "-u", unit.String(), "-n", strconv.Itoa(request.Tail), "-o", "json", "--no-pager", "--all"}
	if request.Since != "" {
		since, _ := time.Parse(time.RFC3339, request.Since)
		args = append(args, "--since", since.UTC().Format(time.RFC3339Nano))
	}
	out, err := r.Executor.Execute(ctx, localexec.Command{Path: "journalctl", Args: args, Timeout: ReadTimeout})
	if err != nil {
		var execution *localexec.Error
		if errors.As(err, &execution) && (execution.Kind == localexec.NotFound || execution.Kind == localexec.Failed && execution.ExitCode == 1 && strings.TrimSpace(out.Stderr) == "No journal files were opened due to insufficient permissions.") {
			return nil, result.New(result.LogsJournalUnavailable, err)
		}
		return nil, collectionError(err, result.LogsJournalFailed, result.LogsJournalTimeout)
	}
	if err := ctx.Err(); err != nil {
		return nil, collectionError(err, result.LogsJournalFailed, result.LogsJournalTimeout)
	}
	if out.Truncated {
		return nil, result.New(result.LogsTruncated, nil)
	}
	if len(out.Stdout) > MaxBytes {
		return nil, result.New(result.LogsLimitExceeded, nil)
	}
	return parse(out.Stdout, request.Tail)
}
func collectionError(err error, failed, timedOut result.Code) *result.Error {
	if errors.Is(err, context.Canceled) {
		return result.New(result.Interrupted, err)
	}
	var execution *localexec.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &execution) && execution.Kind == localexec.Timeout {
		return result.New(timedOut, err)
	}
	return result.New(failed, err)
}

func ownedUnit(snapshot target.Snapshot, app string) (systemd.Unit, error) {
	refuse := func() (systemd.Unit, error) { return systemd.Unit{}, result.New(result.LogsOwnershipRefused, nil) }
	if snapshot.Apps.Status != target.KnownStatus || snapshot.Apps.Value == nil {
		return refuse()
	}
	found := ""
	for _, entry := range *snapshot.Apps.Value {
		if entry.Name != app {
			continue
		}
		if entry.QuadletUnits.Status != target.KnownStatus || entry.QuadletUnits.Value == nil {
			return refuse()
		}
		for _, unit := range *entry.QuadletUnits.Value {
			if unit.Name != app+".container" && unit.Name != "brine-"+app+".container" {
				continue
			}
			if found != "" {
				return refuse()
			}
			found = strings.TrimSuffix(unit.Name, ".container") + ".service"
		}
	}
	if found == "" {
		return refuse()
	}
	return systemd.ParseUnit(found)
}
func parse(raw string, tail int) ([]Line, error) {
	fail := func() ([]Line, error) { return nil, result.New(result.LogsInvalidJournal, nil) }
	if len(raw) > MaxBytes {
		return nil, result.New(result.LogsLimitExceeded, nil)
	}
	lines := make([]Line, 0)
	var redact redactor
	for _, record := range strings.Split(raw, "\n") {
		if strings.TrimSpace(record) == "" {
			continue
		}
		if len(lines) >= tail {
			return nil, result.New(result.LogsLimitExceeded, nil)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(record), &fields); err != nil {
			return fail()
		}
		var timestamp, priority, message string
		if json.Unmarshal(fields["__REALTIME_TIMESTAMP"], &timestamp) != nil || json.Unmarshal(fields["PRIORITY"], &priority) != nil {
			return fail()
		}
		micros, err := strconv.ParseInt(timestamp, 10, 64)
		if err != nil || micros < 0 || micros > 253402300799999999 {
			return fail()
		}
		p, err := strconv.Atoi(priority)
		if err != nil || p < 0 || p > 7 {
			return fail()
		}
		message, err = strictjson.Value[string](fields["MESSAGE"])
		if err != nil {
			var binary []uint8
			if json.Unmarshal(fields["MESSAGE"], &binary) != nil || binary == nil {
				return fail()
			}
			message = string(binary)
		}
		lines = append(lines, Line{Timestamp: time.UnixMicro(micros).UTC().Format(time.RFC3339Nano), Priority: p, Message: redact.clean(message)})
	}
	encoded, err := json.Marshal(lines)
	if err != nil {
		return fail()
	}
	if len(encoded) > MaxBytes {
		return nil, result.New(result.LogsLimitExceeded, nil)
	}
	return lines, nil
}

// DecodeLines validates and re-sanitizes the transport boundary, not just journal output.
func DecodeLines(raw []byte) ([]Line, error) {
	if len(raw) > MaxBytes {
		return nil, result.New(result.TransportInvalidResponse, nil)
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil || entries == nil || len(entries) > MaxTail {
		return nil, result.New(result.TransportInvalidResponse, nil)
	}
	lines := make([]Line, 0, len(entries))
	var r redactor
	for _, entry := range entries {
		fields, err := strictjson.Object(entry, "timestamp", "priority", "message")
		if err != nil {
			return nil, result.New(result.TransportInvalidResponse, nil)
		}
		timestamp, err := strictjson.Value[string](fields["timestamp"])
		if err != nil {
			return nil, result.New(result.TransportInvalidResponse, nil)
		}
		priority, err := strictjson.Value[int](fields["priority"])
		if err != nil || priority < 0 || priority > 7 {
			return nil, result.New(result.TransportInvalidResponse, nil)
		}
		message, err := strictjson.Value[string](fields["message"])
		if err != nil {
			return nil, result.New(result.TransportInvalidResponse, nil)
		}
		parsed, err := time.Parse(time.RFC3339Nano, timestamp)
		if err != nil {
			return nil, result.New(result.TransportInvalidResponse, nil)
		}
		lines = append(lines, Line{Timestamp: parsed.UTC().Format(time.RFC3339Nano), Priority: priority, Message: r.clean(message)})
	}
	return lines, nil
}
