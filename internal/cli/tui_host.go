package cli

import (
	"context"
	"encoding/json"

	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/logs"
	"github.com/ShaulLavo/brine/internal/result"
)

// tuiLogTail is the bounded number of log entries one TUI read requests.
const tuiLogTail = 100

// tuiHost is the TUI's only access to a target. Each method is one existing
// dispatcher op, validated exactly like its CLI command.
type tuiHost interface {
	Plan(context.Context, tuiAction, string) (tuiReview, error)
	DeploymentPlan(context.Context, string) (tuiReview, error)
	Apply(context.Context, string, string) (jobs.Accepted, error)
	Apps(ctx context.Context) (apps.Report, error)
	Logs(ctx context.Context, app string) ([]logs.Line, error)
	Operation(ctx context.Context, id string, cursor uint64) (jobs.Status, error)
}

// connectedHost reads one selected target. Every read loads and checks the
// pinned target file again through operationFlags.call, so a target picked in
// the TUI reports a bad file as that read's error.
type connectedHost struct {
	flags operationFlags
	deps  Dependencies
}

func (h connectedHost) Apps(ctx context.Context) (apps.Report, error) {
	return readAppReport(ctx, h.deps, h.flags, "")
}

func (h connectedHost) Logs(ctx context.Context, app string) ([]logs.Line, error) {
	return readLogLines(ctx, h.deps, h.flags, logs.Request{App: app, Tail: tuiLogTail})
}

// readLogLines reads one bounded tail and repeats the client-side decode,
// redaction and bound checks that the logs command applies.
func readLogLines(ctx context.Context, deps Dependencies, flags operationFlags, request logs.Request) ([]logs.Line, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	response, err := flags.call(ctx, deps, "logs", request)
	if err != nil {
		return nil, err
	}
	if !response.OK {
		return nil, result.New(result.TransportInvalidResponse, nil)
	}
	raw, err := json.Marshal(response.Data)
	if err != nil {
		return nil, result.New(result.TransportInvalidResponse, nil)
	}
	lines, err := logs.DecodeLines(raw)
	if err != nil {
		return nil, err
	}
	if len(lines) > request.Tail {
		return nil, result.New(result.TransportInvalidResponse, nil)
	}
	return lines, nil
}

func (h connectedHost) Operation(ctx context.Context, id string, cursor uint64) (jobs.Status, error) {
	return readOperationPage(ctx, h.deps, h.flags, id, cursor)
}
