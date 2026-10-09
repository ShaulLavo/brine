package logs

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/strictjson"
	"github.com/ShaulLavo/brine/internal/systemd"
)

var containerIDPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (r Reader) readContainer(ctx context.Context, request Request, unit systemd.Unit) ([]Line, error) {
	name := "systemd-" + strings.TrimSuffix(unit.String(), ".service")
	inspect, err := r.Executor.Execute(ctx, localexec.Command{Path: "podman", Args: []string{"--remote=false", "inspect", "--type", "container", "--format", `{"id":{{json .ID}},"name":{{json .Name}},"unit":{{json (index .Config.Labels "PODMAN_SYSTEMD_UNIT")}},"driver":{{json .HostConfig.LogConfig.Type}}}`, name}, Timeout: ReadTimeout})
	if err != nil {
		return nil, containerError(err)
	}
	if err := ctx.Err(); err != nil {
		return nil, containerError(err)
	}
	if inspect.Truncated {
		return nil, result.New(result.LogsTruncated, nil)
	}
	if len(inspect.Stdout) > MaxBytes {
		return nil, result.New(result.LogsLimitExceeded, nil)
	}
	fields, err := strictjson.Object([]byte(strings.TrimSpace(inspect.Stdout)), "id", "name", "unit", "driver")
	if err != nil {
		return nil, result.New(result.LogsOwnershipRefused, err)
	}
	id, idErr := strictjson.Value[string](fields["id"])
	observedName, nameErr := strictjson.Value[string](fields["name"])
	observedUnit, unitErr := strictjson.Value[string](fields["unit"])
	if idErr != nil || !containerIDPattern.MatchString(id) || nameErr != nil || unitErr != nil || observedName != name || observedUnit != unit.String() {
		return nil, result.New(result.LogsOwnershipRefused, nil)
	}
	driver, err := strictjson.Value[string](fields["driver"])
	if err != nil || driver != ContainerLogDriver {
		return nil, result.New(result.LogsContainerUnavailable, nil)
	}
	args := []string{"--remote=false", "logs", "--timestamps", "--tail", strconv.Itoa(request.Tail)}
	if request.Since != "" {
		since, _ := time.Parse(time.RFC3339, request.Since)
		args = append(args, "--since", since.UTC().Format(time.RFC3339Nano))
	}
	args = append(args, id)
	out, err := r.Executor.Execute(ctx, localexec.Command{Path: "podman", Args: args, Timeout: ReadTimeout})
	if err != nil {
		return nil, containerError(err)
	}
	if err := ctx.Err(); err != nil {
		return nil, containerError(err)
	}
	if out.Truncated {
		return nil, result.New(result.LogsTruncated, nil)
	}
	return parseContainer(out, request.Tail)
}

func containerError(err error) *result.Error {
	var execution *localexec.Error
	if errors.As(err, &execution) && execution.Kind == localexec.NotFound {
		return result.New(result.LogsContainerUnavailable, err)
	}
	return collectionError(err, result.LogsContainerFailed, result.LogsContainerTimeout)
}

func parseContainer(out localexec.Result, tail int) ([]Line, error) {
	if len(out.Stdout)+len(out.Stderr) > MaxBytes {
		return nil, result.New(result.LogsLimitExceeded, nil)
	}
	type entry struct {
		time     time.Time
		priority int
		message  string
	}
	entries := make([]entry, 0)
	for _, stream := range []struct {
		raw      string
		priority int
	}{{out.Stdout, 6}, {out.Stderr, 3}} {
		records := strings.Split(stream.raw, "\n")
		for i, record := range records {
			if record == "" && i == len(records)-1 {
				continue
			}
			if len(entries) >= tail {
				return nil, result.New(result.LogsLimitExceeded, nil)
			}
			timestamp, message, ok := strings.Cut(record, " ")
			if !ok || !sincePattern.MatchString(timestamp) {
				return nil, result.New(result.LogsInvalidContainer, nil)
			}
			parsed, err := time.Parse(time.RFC3339Nano, timestamp)
			if err != nil {
				return nil, result.New(result.LogsInvalidContainer, err)
			}
			entries = append(entries, entry{time: parsed, priority: stream.priority, message: message})
		}
	}
	slices.SortStableFunc(entries, func(a, b entry) int { return a.time.Compare(b.time) })
	lines := make([]Line, 0, len(entries))
	var redact redactor
	for _, e := range entries {
		lines = append(lines, Line{Timestamp: e.time.UTC().Format(time.RFC3339Nano), Priority: e.priority, Message: redact.clean(e.message)})
	}
	raw, err := json.Marshal(lines)
	if err != nil || len(raw) > MaxBytes {
		return nil, result.New(result.LogsLimitExceeded, nil)
	}
	return lines, nil
}
