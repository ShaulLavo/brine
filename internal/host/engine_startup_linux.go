//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/systemd"
)

type EngineStartupRuntime struct {
	state     *store.Store
	session   localexec.Session
	stateRoot string
	home      string
}

func OpenEngineStartup(ctx context.Context) (*EngineStartupRuntime, error) {
	uid := os.Geteuid()
	identity, err := user.LookupId(strconv.Itoa(uid))
	if err != nil || uid <= 0 || int64(uid) > math.MaxUint32 || identity.Username != "brine" || identity.HomeDir != "/home/brine" {
		return nil, errEngineSource
	}
	stateRoot := filepath.Join(identity.HomeDir, ".local/state/brine")
	state, err := store.OpenReadOnly(ctx, stateRoot)
	if err != nil {
		return nil, errEngineSource
	}
	session, err := localexec.NewSession(localexec.ExecRunner{}, uint32(uid), identity.HomeDir, 5*time.Second)
	if err != nil {
		_ = state.Close()
		return nil, errEngineSource
	}
	return &EngineStartupRuntime{state: state, session: session, stateRoot: stateRoot, home: identity.HomeDir}, nil
}

func (r *EngineStartupRuntime) Close() error { return r.state.Close() }

type engineStartGuard struct {
	root     string
	binding  data.ReplicaBindingID
	launch   *data.EngineLock
	lifetime *data.EngineLock
}

func (g *engineStartGuard) active() bool {
	return g != nil && g.launch.HoldsLaunch(g.root, g.binding) && g.lifetime.HoldsLifetime(g.root, g.binding)
}

func (g *engineStartGuard) close() error {
	return errors.Join(g.launch.Release(), g.lifetime.Release())
}

func acquireEngineStart(ctx context.Context, root string, binding data.ReplicaBindingID) (*engineStartGuard, error) {
	launch, err := data.AcquireEngineLaunchGuard(ctx, root, binding)
	if err != nil {
		return nil, errEngineSource
	}
	lifetime, err := data.AcquireEngineLifetimeLock(ctx, root, binding)
	if err != nil {
		_ = launch.Release()
		return nil, errEngineSource
	}
	return &engineStartGuard{root: root, binding: binding, launch: launch, lifetime: lifetime}, nil
}

type engineStartupProof struct {
	guard    *engineStartGuard
	permit   store.EnginePermit
	copy     *engineSourceCopy
	material engineMaterial
	self     systemd.StarterProcess
}

// EngineExec is a service-only foreground entry. Dispatcher/deploy admission is
// separate; no client can supply a command, image, mount, key or runtime port.
func (r *EngineStartupRuntime) EngineExec(ctx context.Context, binding string) error {
	if !data.ValidID(binding) {
		return errEngineSource
	}
	permit, err := r.state.ReadEnginePermit(ctx, data.ReplicaBindingID(binding))
	if err != nil || !permit.AllowsVerification(time.Now().UTC()) {
		return errEngineSource
	}
	startup, cancel := context.WithTimeout(ctx, time.Duration(permit.Runtime.StartupSeconds)*time.Second)
	defer cancel()
	guard, err := acquireEngineStart(startup, r.stateRoot, data.ReplicaBindingID(binding))
	if err != nil {
		return err
	}
	defer func() { _ = guard.close() }()
	proof, err := r.verifyEngineStart(startup, guard)
	if err != nil {
		return err
	}
	defer proof.material.clear()
	defer func() { _ = proof.copy.discard() }()
	return r.launchEngine(ctx, startup, proof)
}

func (r *EngineStartupRuntime) verifyEngineStart(ctx context.Context, guard *engineStartGuard) (engineStartupProof, error) {
	if !guard.active() {
		return engineStartupProof{}, errEngineSource
	}
	permit, err := r.state.ReadEnginePermit(ctx, guard.binding)
	if err != nil || !permit.AllowsVerification(time.Now().UTC()) || verifyEngineService(ctx, r.home, permit) != nil {
		return engineStartupProof{}, errEngineSource
	}
	self, err := engineStarterProcess()
	if err != nil || r.starterOwned(ctx, permit, self) != nil {
		return engineStartupProof{}, errEngineSource
	}
	containers, err := listEngineContainers(ctx, r.session)
	if err != nil || !engineBindingAbsent(containers, permit) {
		return engineStartupProof{}, errEngineSource
	}
	root, err := data.InspectRoot(string(permit.Reserved.Database.Root))
	if err != nil || permit.Allocation == nil || root.Device != permit.Allocation.RootDevice || root.Inode != permit.Allocation.RootInode {
		return engineStartupProof{}, errEngineSource
	}
	material, err := readEngineMaterial(ctx, r.stateRoot, permit)
	if err != nil {
		return engineStartupProof{}, err
	}
	nonce, err := data.NewID()
	if err != nil {
		material.clear()
		return engineStartupProof{}, errEngineSource
	}
	staging := filepath.Join(r.stateRoot, "engine-verification")
	copy, err := copyEngineSource(ctx, permit.Reserved.Database, staging, nonce, engineCopyLimits{Bytes: permit.Runtime.CopyBytes, Files: permit.Runtime.CopyFiles, Depth: 64})
	if err != nil {
		material.clear()
		return engineStartupProof{}, err
	}
	proof := engineStartupProof{guard: guard, permit: permit, copy: copy, material: material, self: self}
	if err = r.privateEngineProof(ctx, proof, nonce); err != nil {
		material.clear()
		_ = copy.discard()
		return engineStartupProof{}, err
	}
	material.clear()
	if err = copy.disposeStage(); err != nil {
		_ = copy.discard()
		return engineStartupProof{}, errEngineSource
	}
	return proof, nil
}

func (r *EngineStartupRuntime) starterOwned(ctx context.Context, permit store.EnginePermit, self systemd.StarterProcess) error {
	current, err := engineStarterProcess()
	if err != nil || current != self || !currentEngineStarterAlone(self.PID, self.ControlGroup) {
		return errEngineSource
	}
	unit, err := systemd.ParseUnit(engineContainerName(permit.Runtime.BindingID) + ".service")
	if err != nil {
		return errEngineSource
	}
	if _, err = systemd.New(r.session).StartupInvocation(ctx, unit, self); err != nil {
		return errEngineSource
	}
	return verifyEngineLoadedService(ctx, r.session, r.home, unit)
}

func (r *EngineStartupRuntime) privateEngineProof(ctx context.Context, proof engineStartupProof, nonce string) error {
	if !proof.guard.active() || !engineModuleHTTPDisabled(proof.copy.root) {
		return errEngineSource
	}
	allocation := proof.permit.Allocation
	if allocation == nil || proof.copy.original.root.Inode != allocation.DirectoryInode || proof.copy.original.root.Device != allocation.DirectoryDevice {
		return errEngineSource
	}
	if err := proof.copy.root.RemoveAll("program-bytes"); err != nil {
		return errEngineSource
	}
	args, err := privateEngineArguments(proof.permit, proof.material, proof.copy, nonce, "/usr/local/bin/brine")
	if err != nil {
		return err
	}
	input, err := json.Marshal(engineReplayInput{Runtime: proof.permit.Runtime, Token: string(proof.material.publisher)})
	if err != nil {
		return errEngineSource
	}
	defer clear(input)
	result, runErr := (localexec.ExecRunner{}).Execute(ctx, localexec.Command{Path: "/usr/bin/podman", Args: args, Stdin: input, Timeout: time.Duration(proof.permit.Runtime.StartupSeconds) * time.Second, Mutation: true})
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	disposeErr := privateEngineDisposed(cleanup, r.session, nonce)
	if disposeErr != nil {
		r.retainEngineLifetime(ctx, proof.permit, nil)
		return errEngineSource
	}
	if runErr != nil || result.Truncated {
		return errEngineSource
	}
	observed, err := decodeEngineReplayObservation([]byte(result.Stdout))
	if err != nil || !observed.matches(proof.permit.Runtime) {
		return errEngineSource
	}
	return proof.copy.sourceUnchanged(ctx)
}

func (r *EngineStartupRuntime) finalEngineCheck(ctx context.Context, proof engineStartupProof) error {
	if !proof.guard.active() || proof.copy.sourceUnchanged(ctx) != nil || r.starterOwned(ctx, proof.permit, proof.self) != nil {
		return errEngineSource
	}
	current, err := r.state.ReadEnginePermit(ctx, proof.guard.binding)
	if err != nil || !current.AllowsVerification(time.Now().UTC()) || !sameEnginePermit(current, proof.permit) || verifyEngineService(ctx, r.home, current) != nil {
		return errEngineSource
	}
	material, err := readEngineMaterial(ctx, r.stateRoot, current)
	if err != nil {
		return err
	}
	material.clear()
	containers, err := listEngineContainers(ctx, r.session)
	if err != nil || !engineBindingAbsent(containers, current) {
		return errEngineSource
	}
	return nil
}

func (r *EngineStartupRuntime) launchEngine(ctx, startup context.Context, proof engineStartupProof) error {
	if err := r.finalEngineCheck(startup, proof); err != nil {
		return err
	}
	if err := removeStoppedEngine(startup, r.session, proof.permit); err != nil {
		return err
	}
	args, err := liveEngineArguments(proof.permit, proof.material)
	if err != nil {
		return err
	}
	command, err := localexec.StartForeground(context.WithoutCancel(ctx), localexec.ForegroundCommand{Path: "/usr/bin/podman", Args: args, Env: []string{"PATH=/usr/bin:/bin", "HOME=" + r.home, "XDG_RUNTIME_DIR=/run/user/" + strconv.Itoa(os.Geteuid()), "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/" + strconv.Itoa(os.Geteuid()) + "/bus", "LC_ALL=C"}, Dir: r.home})
	if err != nil {
		return errEngineSource
	}
	exited := command.Done()
	native, err := r.waitEngineHandoff(startup, proof.permit, proof.material, exited)
	if err != nil {
		_ = r.stopLiveEngine(context.WithoutCancel(ctx), proof.permit)
		_ = command.KillGroup()
		r.retainEngineLifetime(ctx, proof.permit, native)
		return errEngineSource
	}
	defer native.close()
	if !proof.guard.active() || native.alive() != nil {
		_ = r.stopLiveEngine(context.WithoutCancel(ctx), proof.permit)
		r.retainEngineLifetime(ctx, proof.permit, native)
		return errEngineSource
	}
	if err = proof.guard.launch.Release(); err != nil {
		_ = r.stopLiveEngine(context.WithoutCancel(ctx), proof.permit)
		r.retainEngineLifetime(ctx, proof.permit, native)
		return errEngineSource
	}
	if err = engineReadyNotify(startup); err != nil {
		_ = r.stopLiveEngine(context.WithoutCancel(ctx), proof.permit)
		r.retainEngineLifetime(ctx, proof.permit, native)
		return errEngineSource
	}
	select {
	case err = <-exited:
	case <-ctx.Done():
		if err = r.stopLiveEngine(context.WithoutCancel(ctx), proof.permit); err != nil {
			r.retainEngineLifetime(ctx, proof.permit, native)
			return err
		}
		select {
		case err = <-exited:
		case <-time.After(time.Duration(proof.permit.Runtime.StopSeconds) * time.Second):
			r.retainEngineLifetime(ctx, proof.permit, native)
			return errEngineSource
		}
	}
	if absenceErr := r.liveEngineAbsent(context.WithoutCancel(ctx), proof.permit); absenceErr != nil || !native.exited() {
		r.retainEngineLifetime(ctx, proof.permit, native)
		return errEngineSource
	}
	return err
}

// An unknown stop keeps the foreground supervisor and its lifetime descriptor.
// A forced manager kill can release it; subsequent startup still requires actual
// source-process absence and cannot infer absence from that released descriptor.
func (r *EngineStartupRuntime) retainEngineLifetime(ctx context.Context, permit store.EnginePermit, native *engineNativeHandle) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		err := r.liveEngineAbsent(ctx, permit)
		cancel()
		if err == nil && (native == nil || native.exited()) {
			return
		}
		<-ticker.C
	}
}

func engineReadyNotify(ctx context.Context) error {
	address := os.Getenv("NOTIFY_SOCKET")
	expected := "/run/user/" + strconv.Itoa(os.Geteuid()) + "/systemd/notify"
	if address != expected {
		return errEngineSource
	}
	connection, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unixgram", address)
	if err != nil {
		return errEngineSource
	}
	defer func() { _ = connection.Close() }()
	if err = connection.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		return errEngineSource
	}
	_, err = connection.Write([]byte("READY=1"))
	return err
}
