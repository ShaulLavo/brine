package inventory

import (
	"context"
	"sort"
	"strings"

	"github.com/ShaulLavo/brine/internal/target"
)

func (c Collector) runtimeApp(ctx context.Context, name string, units []target.Unit, runner bool, publications map[string]publication, inactive map[string]bool, secrets []target.Secret) target.App {
	app := target.App{UDPPublication: unknown[target.UDPPublication](), Name: name, Image: unknown[target.Image](), AllocatedHostPort: unknown[target.Port](), QuadletUnits: target.Known(units), Secrets: unknown[[]target.Secret]()}
	if !runner {
		return app
	}
	if len(units) > 0 {
		c.appPublication(ctx, &app, units, publications, inactive)
	}
	observed := []target.Secret{}
	for _, secret := range secrets {
		if strings.HasPrefix(secret.Name, "brine."+name+".") {
			observed = append(observed, secret)
		}
	}
	sort.Slice(observed, func(i, j int) bool { return observed[i].Name < observed[j].Name })
	app.Secrets = target.Known(observed)
	return app
}

func (c Collector) appPublication(ctx context.Context, app *target.App, units []target.Unit, publications map[string]publication, inactive map[string]bool) {
	activity, stopped := c.appActivity(ctx, units)
	app.UnitActive = &activity
	inactive[app.Name] = stopped
	measured := c.livePublication(ctx, units)
	if stopped && (measured.Status == target.Absent || c.containerAbsent(ctx, app.Name)) {
		app.UDPPublication = absent[target.UDPPublication]()
	}
	if measured.Status != target.KnownStatus || stopped {
		return
	}
	publications[app.Name] = *measured.Value
	app.AllocatedHostPort = target.Known(measured.Value.Host)
	app.UDPPublication = absent[target.UDPPublication]()
	if measured.Value.hasUDP() {
		app.UDPPublication = target.Known(measured.Value.udpPublication())
	}
}

func (c Collector) appActivity(ctx context.Context, units []target.Unit) (target.Observation[bool], bool) {
	name := ""
	for _, unit := range units {
		if !strings.HasSuffix(unit.Name, ".container") {
			continue
		}
		if name != "" {
			return unknown[bool](), false
		}
		name = strings.TrimSuffix(unit.Name, ".container")
	}
	if name == "" {
		return unknown[bool](), false
	}
	state, err := c.probe(ctx, scalarOutputLimit, "systemctl", "--user", "show", name+".service", "--property=ActiveState", "--value")
	if err != nil {
		return unknown[bool](), false
	}
	switch state {
	case "active", "reloading", "refreshing":
		return target.Known(true), false
	case "inactive":
		return target.Known(false), true
	case "failed", "activating", "deactivating", "maintenance":
		return target.Known(false), false
	default:
		return unknown[bool](), false
	}
}
