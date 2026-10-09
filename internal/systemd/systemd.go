// Package systemd controls runner user services and only reloads system Caddy.
package systemd

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/ShaulLavo/brine/internal/localexec"
)

type Unit struct{ value string }

func (u Unit) String() string { return u.value }

var unitPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.:-]*(?:@[a-zA-Z0-9_.:-]*)?\.service$`)

func ParseUnit(s string) (Unit, error) {
	if len(s) > 255 || !unitPattern.MatchString(s) {
		return Unit{}, &localexec.Error{Kind: localexec.Invalid}
	}
	return Unit{s}, nil
}

type Properties struct {
	ActiveState, SubState                    string
	NRestarts, ActiveEnterTimestampMonotonic uint64
}
type Adapter interface {
	DaemonReload(context.Context) error
	Start(context.Context, Unit) error
	Stop(context.Context, Unit) error
	Restart(context.Context, Unit) error
	IsActive(context.Context, Unit) (bool, error)
	Show(context.Context, Unit) (Properties, error)
	JobPending(context.Context, Unit) (bool, error)
	ReloadCaddy(context.Context) error
}
type Client struct{ session localexec.Session }

func New(session localexec.Session) *Client { return &Client{session} }
func (c *Client) run(ctx context.Context, args []string, mutation bool) (localexec.Result, error) {
	return c.session.Execute(ctx, "systemctl", args, nil, mutation)
}
func (c *Client) DaemonReload(ctx context.Context) error {
	_, e := c.run(ctx, []string{"--user", "daemon-reload"}, true)
	return e
}
func (c *Client) action(ctx context.Context, action string, u Unit) error {
	if u.value == "" {
		return &localexec.Error{Kind: localexec.Invalid}
	}
	// Show distinguishes missing units without parsing localized diagnostics.
	if _, e := c.Show(ctx, u); e != nil {
		return e
	}
	_, e := c.run(ctx, []string{"--user", action, u.value}, true)
	return e
}
func (c *Client) Start(ctx context.Context, u Unit) error   { return c.action(ctx, "start", u) }
func (c *Client) Stop(ctx context.Context, u Unit) error    { return c.action(ctx, "stop", u) }
func (c *Client) Restart(ctx context.Context, u Unit) error { return c.action(ctx, "restart", u) }
func (c *Client) ReloadCaddy(ctx context.Context) error {
	_, e := c.run(ctx, []string{"reload", "caddy.service"}, true)
	return e
}
func (c *Client) IsActive(ctx context.Context, u Unit) (bool, error) {
	if u.value == "" {
		return false, &localexec.Error{Kind: localexec.Invalid}
	}
	r, e := c.run(ctx, []string{"--user", "is-active", u.value}, false)
	state := strings.TrimSpace(r.Stdout)
	if e == nil {
		if state != "active" && state != "reloading" && state != "refreshing" {
			return false, &localexec.Error{Kind: localexec.Failed}
		}
		return true, nil
	}
	var re *localexec.Error
	if errors.As(e, &re) && re.Kind == localexec.Failed {
		if re.ExitCode == 4 && state == "unknown" {
			return false, &localexec.Error{Kind: localexec.NotFound, ExitCode: 4}
		}
		if re.ExitCode == 3 {
			switch state {
			case "inactive", "failed", "activating", "deactivating", "maintenance":
				// systemd 257 also reports missing units as inactive with exit 3.
				_, err := c.Show(ctx, u)
				return false, err
			}
		}
	}
	return false, e
}
func (c *Client) Show(ctx context.Context, u Unit) (Properties, error) {
	if u.value == "" {
		return Properties{}, &localexec.Error{Kind: localexec.Invalid}
	}
	r, e := c.run(ctx, []string{"--user", "show", u.value, "--property=LoadState,ActiveState,SubState,NRestarts,ActiveEnterTimestampMonotonic"}, false)
	if e != nil {
		return Properties{}, e
	}
	return parseProperties(r.Stdout)
}

// JobPending observes the manager job attached to this exact unit. systemctl
// serializes an absent job as an empty property, not the D-Bus numeric zero.
// Labeled properties distinguish that evidence from missing or failed output.
func (c *Client) JobPending(ctx context.Context, u Unit) (bool, error) {
	if u.value == "" {
		return false, &localexec.Error{Kind: localexec.Invalid}
	}
	r, err := c.run(ctx, []string{"--user", "show", u.value, "--property=LoadState,Job"}, false)
	if err != nil {
		return false, err
	}
	bad := func() (bool, error) { return false, &localexec.Error{Kind: localexec.Failed} }
	fields := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(r.Stdout), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || (key != "LoadState" && key != "Job") {
			return bad()
		}
		if _, duplicate := fields[key]; duplicate {
			return bad()
		}
		fields[key] = value
	}
	job, present := fields["Job"]
	if !present || (fields["LoadState"] != "loaded" && fields["LoadState"] != "not-found") {
		return bad()
	}
	if job == "" {
		return false, nil
	}
	if strings.IndexFunc(job, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
		return bad()
	}
	id, err := strconv.ParseUint(job, 10, 32)
	if err != nil {
		return bad()
	}
	return id != 0, nil
}

func parseProperties(output string) (Properties, error) {
	bad := func() (Properties, error) { return Properties{}, &localexec.Error{Kind: localexec.Failed} }
	fields := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return bad()
		}
		switch key {
		case "LoadState", "ActiveState", "SubState", "NRestarts", "ActiveEnterTimestampMonotonic":
		default:
			return bad()
		}
		if _, exists := fields[key]; exists {
			return bad()
		}
		fields[key] = value
	}
	if fields["LoadState"] == "not-found" {
		return Properties{}, &localexec.Error{Kind: localexec.NotFound}
	}
	if len(fields) != 5 || fields["LoadState"] == "" || fields["ActiveState"] == "" || fields["SubState"] == "" {
		return bad()
	}
	restarts, e := strconv.ParseUint(fields["NRestarts"], 10, 64)
	if e != nil {
		return bad()
	}
	timestamp, e := strconv.ParseUint(fields["ActiveEnterTimestampMonotonic"], 10, 64)
	if e != nil {
		return bad()
	}
	return Properties{ActiveState: fields["ActiveState"], SubState: fields["SubState"], NRestarts: restarts, ActiveEnterTimestampMonotonic: timestamp}, nil
}

var _ Adapter = (*Client)(nil)
