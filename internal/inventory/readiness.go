package inventory

import (
	"context"
	"strconv"
	"strings"

	"github.com/ShaulLavo/brine/internal/target"
)

// ReadinessFacts extends inventory with fixed, read-only runtime observations.
// It carries neither subprocess output nor permission to repair the host.
type ReadinessFacts struct {
	UIDMap        target.Observation[bool] `json:"uidmap"`
	DBus          target.Observation[bool] `json:"dbus_user_session"`
	Polkit        target.Observation[bool] `json:"polkitd"`
	UserBus       target.Observation[bool] `json:"user_bus"`
	CaddyActive   target.Observation[bool] `json:"caddy_active"`
	AdminLoopback target.Observation[bool] `json:"admin_loopback"`
	CatchAll      target.Observation[bool] `json:"catch_all"`
}

func (c Collector) Readiness(ctx context.Context) (ReadinessFacts, error) {
	r := ReadinessFacts{UIDMap: unknown[bool](), DBus: unknown[bool](), Polkit: unknown[bool](), UserBus: unknown[bool](), CaddyActive: unknown[bool](), AdminLoopback: unknown[bool](), CatchAll: unknown[bool]()}
	out, err := c.probe(ctx, scalarOutputLimit, "stat", "--format=%a", "/usr/bin/newuidmap", "/usr/bin/newgidmap")
	if err == nil {
		r.UIDMap = helperModes(out)
	}
	r.DBus = c.installed(ctx, "dbus-user-session")
	r.Polkit = c.installed(ctx, "polkitd")
	out, err = c.probe(ctx, scalarOutputLimit, "systemctl", "--user", "show", "--property=Version", "--value")
	if err == nil && versionToken.MatchString(out) {
		r.UserBus = target.Known(true)
	}
	out, err = c.probe(ctx, scalarOutputLimit, "systemctl", "show", "caddy.service", "--property=ActiveState", "--value")
	if err == nil {
		r.CaddyActive = activeState(out)
	}
	out, err = c.probe(ctx, caddyOutputLimit, "curl", "--disable", "--noproxy", "*", "--silent", "--fail", "--max-time", "2", "http://127.0.0.1:2019/config/")
	if err == nil {
		r.AdminLoopback = adminBinding([]byte(out))
	}
	out, err = c.probe(ctx, caddyOutputLimit, "caddy", "adapt", "--config", "/etc/caddy/Caddyfile", "--adapter", "caddyfile")
	if err == nil {
		attachReadinessCaddy(&r, []byte(out))
	}
	return r, nil
}

func helperModes(out string) target.Observation[bool] {
	modes := strings.Fields(out)
	if len(modes) != 2 {
		return unknown[bool]()
	}
	good := true
	for _, mode := range modes {
		value, err := strconv.ParseUint(mode, 8, 12)
		if err != nil {
			return unknown[bool]()
		}
		good = good && value&04000 != 0 && value&0111 != 0
	}
	return target.Known(good)
}

func (c Collector) installed(ctx context.Context, name string) target.Observation[bool] {
	out, err := c.probe(ctx, scalarOutputLimit, "dpkg-query", "-W", "-f=${db:Status-Status}", name)
	if err != nil {
		return unknown[bool]()
	}
	switch out {
	case "installed":
		return target.Known(true)
	case "not-installed", "config-files", "unpacked", "half-configured", "half-installed":
		return target.Known(false)
	default:
		return unknown[bool]()
	}
}

func activeState(out string) target.Observation[bool] {
	switch out {
	case "active":
		return target.Known(true)
	case "inactive", "failed", "activating", "deactivating", "reloading":
		return target.Known(false)
	default:
		return unknown[bool]()
	}
}

func attachReadinessCaddy(r *ReadinessFacts, raw []byte) {
	disk := adminBinding(raw)
	if disk.Value != nil && !*disk.Value {
		r.AdminLoopback = disk
	}
	domains, ok := caddyDomains(raw)
	if !ok {
		return
	}
	catchAll := false
	for _, domain := range domains {
		catchAll = catchAll || domain == "*"
	}
	r.CatchAll = target.Known(catchAll)
}

// CollectReadiness reuses host decoders without opening Podman's application
// storage through secret, container or image listing commands.
func (c Collector) CollectReadiness(ctx context.Context) (target.Snapshot, error) {
	c.hostOnly = true
	return c.Collect(ctx)
}
