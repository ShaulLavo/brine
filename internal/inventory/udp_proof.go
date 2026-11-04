package inventory

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/target"
	"github.com/ShaulLavo/brine/internal/udpready"
)

// UDPReady repeats the physical collector's PID/start-time/cgroup/FD/inode
// checks against a caller-validated immutable unit. It does not change the
// committed-only control path or manufacture status socket ownership.
func (c Collector) UDPReady(ctx context.Context, app, planID string, allocation plan.UDPAllocation, units []target.Unit) error {
	if c.RunnerUser == "" {
		c.RunnerUser = "brine"
	}
	s, err := c.Collect(ctx)
	if err != nil {
		return err
	}
	home, exists := c.runner(ctx, &s)
	if !exists || !safeToken.MatchString(app) || !renderedUnitMarker.MatchString("# Brine-owned plan="+planID+"\n") {
		return errors.New("UDP proof identity unavailable")
	}
	var unit target.Unit
	for _, u := range units {
		if u.Name == app+".container" {
			unit = u
		}
	}
	raw, err := c.FS.ReadFile(ctx, filepath.Join(home, ".config/containers/systemd", unit.Name))
	if err != nil || unit.Hash == "" || digest(raw) != unit.Hash || !strings.HasPrefix(string(raw), "# Brine-owned plan="+planID+"\n") {
		return errors.New("UDP unit identity differs")
	}
	measured, err := c.livePublication(ctx, units)
	if err != nil || measured.Status != target.KnownStatus || measured.Value == nil {
		return errors.New("UDP runtime mapping unavailable")
	}
	publications := map[string]publication{app: *measured.Value}
	c.listeners(ctx, &s, home, publications)
	if udpready.Check(s, app, allocation, units).Local != udpready.Ready {
		return errors.New("UDP publication not ready")
	}
	return nil
}

// UDPAbsent names one endpoint. Both the installed unit and runtime mapping
// must exclude it, and every overlapping socket must be absent regardless of owner.
func (c Collector) UDPAbsent(ctx context.Context, app string, e plan.UDPEndpoint) error {
	if !safeToken.MatchString(app) || e.Validate() != nil {
		return errors.New("invalid UDP absence identity")
	}
	if c.RunnerUser == "" {
		c.RunnerUser = "brine"
	}
	s, err := c.Collect(ctx)
	if err != nil {
		return err
	}
	if s.UDPSockets.Status != target.KnownStatus || s.UDPSockets.Value == nil {
		return errors.New("UDP sockets unknown")
	}
	for _, socket := range *s.UDPSockets.Value {
		if e.Overlaps(socket.Address, socket.Port) {
			return errors.New("UDP endpoint still has a socket")
		}
	}
	home, exists := c.runner(ctx, &s)
	if !exists || !c.isRunner(ctx, home) {
		return errors.New("UDP runner unknown")
	}
	raw, err := c.FS.ReadFile(ctx, filepath.Join(home, ".config/containers/systemd", app+".container"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err == nil {
		pinned, ok := pinnedListenerPort(string(raw))
		if !ok || !renderedUnitMarker.Match(raw) {
			return errors.New("UDP unit mapping unknown")
		}
		if pinned.hasUDP() && e.Overlaps(pinned.UDPAddress, pinned.UDPHost) {
			return errors.New("UDP endpoint still in unit")
		}
	}
	measured, inspectErr := c.livePublication(ctx, []target.Unit{{Name: app + ".container"}})
	if inspectErr != nil {
		if errors.Is(inspectErr, localexec.ErrOutputLimit) || !c.containerAbsent(ctx, app) {
			return errors.New("UDP container absence unknown")
		}
		return nil
	}
	if measured.Status == target.Absent {
		return nil
	}
	if measured.Status != target.KnownStatus || measured.Value == nil {
		return errors.New("UDP container mapping unknown")
	}
	if measured.Value.hasUDP() && e.Overlaps(measured.Value.UDPAddress, measured.Value.UDPHost) {
		return errors.New("UDP endpoint still in container")
	}
	return nil
}

// RebuildUDP recollects only the plan's own reservation-derived control facts.
// Actual units, containers, sockets, addresses and foreign claims are remeasured.
// family[0] is the observing operation, as the store's UDP family lists it.
func (c Collector) RebuildUDP(ctx context.Context, p plan.Plan, family []string) (target.Snapshot, error) {
	if len(family) == 0 {
		return target.Snapshot{}, errors.New("UDP rebuild requires its operation family")
	}
	return c.collectAs(ctx, family[0], p.Hash, func(state target.ControlInventory) (target.ControlInventory, error) {
		return plan.RebuildUDPControl(state, p, slices.Clone(family))
	})
}
