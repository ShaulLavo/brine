//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/apply"
	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/caddy"
	"github.com/ShaulLavo/brine/internal/diagnose"
	"github.com/ShaulLavo/brine/internal/inventory"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/logs"
	"github.com/ShaulLavo/brine/internal/podman"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/quadlet"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/secrets"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/systemd"
	"github.com/ShaulLavo/brine/internal/target"
)

func Open(ctx context.Context, authenticated string) (*Runtime, error) {
	return openRuntime(ctx, authenticated, false)
}
func OpenPreview(ctx context.Context, authenticated string) (*Runtime, error) {
	return openRuntime(ctx, authenticated, true)
}
func openRuntime(ctx context.Context, authenticated string, preview bool) (_ *Runtime, err error) {
	identity, err := user.LookupId(strconv.Itoa(os.Geteuid()))
	if err != nil || identity.Username != "brine" || identity.HomeDir != "/home/brine" || os.Geteuid() == 0 {
		return nil, result.New(result.DispatchOperationRefused, nil)
	}
	uid, e := strconv.ParseUint(identity.Uid, 10, 32)
	if e != nil {
		return nil, e
	}
	var state *store.Store
	stateDir := filepath.Join(identity.HomeDir, ".local/state/brine")
	if preview {
		read, e := openPreviewState(ctx, stateDir)
		if e != nil {
			return nil, e
		}
		if read.previewStore == nil {
			return read, nil
		}
		state = read.previewStore
	} else {
		state, err = store.Open(stateDir)
	}
	if err != nil {
		return nil, result.New(result.DependencyMissing, err)
	}
	closers := []func() error{state.Close}
	defer func() {
		if err != nil {
			for i := len(closers) - 1; i >= 0; i-- {
				closers[i]()
			}
		}
	}()
	loader := DiskPolicy{}
	_, err = loader.Load(ctx)
	if err != nil {
		return nil, result.New(result.DependencyMissing, err)
	}
	key, err := trustedRead(ctx, "/etc/ssh/brine/inventory-key", 32)
	if err != nil || len(key) != 32 {
		return nil, result.New(result.DependencyMissing, nil)
	}
	collector := inventory.Collector{FS: inventory.HostFS{}, Runner: localexec.ExecRunner{}, IdentityKey: key, StateInventory: state.InventoryState}
	session, err := localexec.NewSession(localexec.ExecRunner{}, uint32(uid), identity.HomeDir, time.Minute)
	if err != nil {
		return nil, err
	}
	runtimeSystemd := systemd.New(session)
	units := lazyUnits{home: identity.HomeDir, validator: quadletValidator{}}
	manager, err := caddy.NewManager("/etc/caddy/brine", caddyValidator{session}, caddyReloader{runtimeSystemd})
	if err != nil {
		return nil, err
	}
	closers = append(closers, manager.Close)
	requester := ""
	if authenticated == "deploy" {
		raw, e := trustedRead(ctx, RequesterPath, 256)
		if e != nil {
			return nil, e
		}
		requester = strings.TrimSpace(string(raw))
		if !regexpRequester(requester) {
			return nil, errors.New("host: invalid requester identity")
		}
	}
	service := Service{Store: state, Inventory: collector, Policy: loader, Images: Registry{}, Requester: requester}
	engine := apply.Executor{Journal: state, Releases: releases{state}, Plans: state, Podman: podman.New(session), Systemd: runtimeSystemd, Units: units, Health: policyHealth{loader}, Routes: apply.GenerationRoutes{
		Manager: manager,
		Main:    func(ctx context.Context) ([]byte, error) { return trustedRead(ctx, "/etc/caddy/Caddyfile", 1<<20) },
		Site: func(d policy.Desired, port target.Port) (caddy.Site, error) {
			p, err := loader.Load(context.Background())
			if err != nil {
				return caddy.Site{}, err
			}
			return caddy.NewSite(App(d), p, spec.Port(port))
		},
	}}
	reconciler := newReconciler(service, engine, runtimeSystemd)
	logReader := logs.Reader{Inventory: collector, Executor: localexec.ExecRunner{}}
	appService := apps.Service{Store: state, Inventory: collector, Probe: apps.HTTPProbe{}, LoadPolicy: loader.Load}
	secretService := secrets.Service{Store: state, Podman: podman.New(session), LoadPolicy: loader.Load, Requester: requester}
	r := &Runtime{Config: appService, Secrets: secretService, Reconciler: reconciler, Inventory: collector, Planner: service, Jobs: jobs.Service{Store: state, Launcher: systemd.NewJobLauncher(session, uint32(uid)), Requester: requester}, Runner: jobs.Runner{Recovery: recoveryJob(reconciler), Reconciler: runnerReconciler{reconciler}, Store: state, Executor: Executor{Service: service, Engine: engine}}, Apps: appService, Logs: logReader, Diagnose: diagnose.Reader{Inventory: collector, Store: state, Logs: logReader, Runner: localexec.ExecRunner{}, FS: inventory.HostFS{}, MinimumFreeDiskBytes: func(ctx context.Context) (uint64, error) {
		p, err := loader.Load(ctx)
		if err != nil {
			return 0, err
		}
		return p.MinimumFreeDiskBytes(), nil
	}}}
	if preview {
		r.Reconciler = readOnlyReconciler{reconciler}
	}
	r.Authorize = service.Authorize
	r.close = func() error {
		var errs []error
		for i := len(closers) - 1; i >= 0; i-- {
			errs = append(errs, closers[i]())
		}
		return errors.Join(errs...)
	}
	return r, nil
}
func regexpRequester(s string) bool {
	return strings.HasPrefix(s, "deploy:") && len(s) == len("deploy:")+64 && digestPattern.MatchString("sha256:"+strings.TrimPrefix(s, "deploy:"))
}

type caddyValidator struct{ session localexec.Session }

func (v caddyValidator) Validate(ctx context.Context, path string) error {
	_, err := v.session.Execute(ctx, "caddy", []string{"validate", "--adapter", "caddyfile", "--config", path}, nil, false)
	return err
}
func (v caddyValidator) Adapt(ctx context.Context, path string) ([]byte, error) {
	out, err := v.session.Execute(ctx, "caddy", []string{"adapt", "--adapter", "caddyfile", "--config", path}, nil, false)
	return []byte(out.Stdout), err
}

type caddyReloader struct{ adapter systemd.Adapter }

func (r caddyReloader) Reload(ctx context.Context) error { return r.adapter.ReloadCaddy(ctx) }

type quadletValidator struct{}

func (quadletValidator) Validate(ctx context.Context, candidate quadlet.Candidate) error {
	out, err := (localexec.ExecRunner{}).Execute(ctx, localexec.Command{Path: "/usr/lib/systemd/system-generators/podman-system-generator", Args: []string{"--user", "--dryrun"}, Env: []string{"QUADLET_UNIT_DIRS=" + candidate.Directory}, Timeout: 10 * time.Second})
	if err != nil || out.Truncated {
		return errors.New("host: Quadlet validation failed")
	}
	if !strings.Contains(out.Stdout, "---"+strings.TrimSuffix(candidate.UnitName, ".container")+".service---") {
		return errors.New("host: generated service missing")
	}
	return nil
}

type policyHealth struct{ loader PolicyLoader }

func (h policyHealth) Check(ctx context.Context, d policy.Desired, port target.Port, routed bool) error {
	p, err := h.loader.Load(ctx)
	if err != nil {
		return err
	}
	if p.Hash() != d.PolicyHash || p.CaddyPort() == 0 {
		return errors.New("host: health policy drift")
	}
	return (apply.HTTPHealth{CaddyPort: p.CaddyPort()}).Check(ctx, d, port, routed)
}

type lazyUnits struct {
	home      string
	validator quadlet.Validator
}

func (u lazyUnits) withManager(fn func(*quadlet.Manager) error) error {
	m, err := quadlet.NewManager(u.home, u.validator)
	if err != nil {
		return err
	}
	defer m.Close()
	return fn(m)
}
func (u lazyUnits) VerifyCurrent(ctx context.Context, name string, hashes ...string) error {
	return quadlet.VerifyCurrent(ctx, u.home, name, hashes...)
}
func (u lazyUnits) Stage(ctx context.Context, unit quadlet.Unit) error {
	return u.withManager(func(m *quadlet.Manager) error { return m.Stage(ctx, unit) })
}
func (u lazyUnits) Install(ctx context.Context, unit quadlet.Unit, old string) error {
	return u.withManager(func(m *quadlet.Manager) error { return m.Install(ctx, unit, old) })
}
func (u lazyUnits) Rollback(ctx context.Context, name, installed, previous string) error {
	return u.withManager(func(m *quadlet.Manager) error { return m.Rollback(ctx, name, installed, previous) })
}

func NewInventory(ctx context.Context) (*inventory.Collector, error) {
	identity, err := user.LookupId(strconv.Itoa(os.Geteuid()))
	if err != nil || identity.Username != "brine" || identity.HomeDir != "/home/brine" || os.Geteuid() == 0 {
		return nil, result.New(result.DispatchOperationRefused, nil)
	}
	key, err := trustedRead(ctx, "/etc/ssh/brine/inventory-key", 32)
	if err != nil || len(key) != 32 {
		return nil, result.New(result.DependencyMissing, nil)
	}
	stateDir := filepath.Join(identity.HomeDir, ".local/state/brine")
	return &inventory.Collector{FS: inventory.HostFS{}, Runner: localexec.ExecRunner{}, IdentityKey: key, StateInventory: func(ctx context.Context) (target.ControlInventory, error) {
		return store.ReadInventoryState(ctx, stateDir)
	}}, nil
}

func (u lazyUnits) VerifyRemove(ctx context.Context, name, hash string) error {
	return u.withManager(func(m *quadlet.Manager) error { return m.VerifyRemove(ctx, name, hash) })
}
func (u lazyUnits) Remove(ctx context.Context, name, hash string) error {
	return u.withManager(func(m *quadlet.Manager) error { return m.Remove(ctx, name, hash) })
}
