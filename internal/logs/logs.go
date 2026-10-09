// Package logs reads bounded, best-effort redacted journals for owned apps.
package logs

import (
	"context"
	"encoding/json"
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

type Request struct {
	App   string `json:"app"`
	Tail  int    `json:"tail"`
	Since string `json:"since,omitempty"`
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

var appName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

func (r Request) Validate() error {
	if !appName.MatchString(r.App) || r.Tail < 1 || r.Tail > MaxTail {
		return result.New(result.InvalidUsage, nil)
	}
	if r.Since != "" {
		if len(r.Since) > 40 {
			return result.New(result.InvalidUsage, nil)
		}
		if _, err := time.Parse(time.RFC3339Nano, r.Since); err != nil {
			return result.New(result.InvalidUsage, nil)
		}
	}
	return nil
}
func DecodeRequest(raw []byte) (Request, error) {
	fields, err := strictjson.Object(raw, "app", "tail", "since")
	if err != nil {
		return Request{}, err
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
	if err := request.Validate(); err != nil {
		return nil, err
	}
	if r.Inventory == nil || r.Executor == nil {
		return nil, result.New(result.DependencyMissing, nil)
	}
	ctx, cancel := context.WithTimeout(ctx, ReadTimeout)
	defer cancel()
	snapshot, err := r.Inventory.Collect(ctx)
	if err != nil {
		return nil, result.Classify(err)
	}
	unit, err := ownedUnit(snapshot, request.App)
	if err != nil {
		return nil, err
	}
	args := []string{"--user", "-u", unit.String(), "-n", strconv.Itoa(request.Tail), "-o", "json", "--no-pager"}
	if request.Since != "" {
		args = append(args, "--since", request.Since)
	}
	out, err := r.Executor.Execute(ctx, localexec.Command{Path: "journalctl", Args: args, Timeout: ReadTimeout})
	if err != nil {
		return nil, result.Classify(err)
	}
	if ctx.Err() != nil {
		return nil, result.Classify(ctx.Err())
	}
	if out.Truncated || len(out.Stdout) > MaxBytes {
		return nil, result.New(result.LogsLimitExceeded, nil)
	}
	return parse(out.Stdout, request.Tail)
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
		if err != nil || micros < 0 {
			return fail()
		}
		p, err := strconv.Atoi(priority)
		if err != nil || p < 0 || p > 7 {
			return fail()
		}
		if err := json.Unmarshal(fields["MESSAGE"], &message); err != nil {
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
	var lines []Line
	if err := json.Unmarshal(raw, &lines); err != nil || lines == nil || len(lines) > MaxTail {
		return nil, result.New(result.TransportInvalidResponse, nil)
	}
	var r redactor
	for i := range lines {
		if _, err := time.Parse(time.RFC3339Nano, lines[i].Timestamp); err != nil || lines[i].Priority < 0 || lines[i].Priority > 7 {
			return nil, result.New(result.TransportInvalidResponse, nil)
		}
		lines[i].Message = r.clean(lines[i].Message)
	}
	return lines, nil
}
