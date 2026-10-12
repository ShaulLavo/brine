//go:build linux

package host

import (
	"context"

	"github.com/ShaulLavo/brine/internal/podman"
	"github.com/ShaulLavo/brine/internal/quadlet"
	"github.com/ShaulLavo/brine/internal/store"
)

// The original20 code intent and existing19 Quadlet replacement retain the
// predecessor across interruption. No service start or route publication occurs.
func (r nativeBackupRuntime) installNativeRestoreRecoveryCode(ctx context.Context, capability *store.NativeRestoreTransition, view store.NativeRestoreTransitionView, tree *engineTransitionTree) error {
	if _, err := r.state.ReadNativeRestoreTransition(ctx, capability); err != nil {
		return err
	}
	recovery, err := r.state.ReadNativeRestoreRecoveryCode(ctx, view.Input.Common().Scope.PlanID)
	if err != nil || recovery.Release.Managed == nil || recovery.SourceRelease.Managed == nil {
		return errEngineSource
	}
	if err = r.nativeRestoreQuiescent(ctx, recovery.App, view.Runtime, tree); err != nil {
		return err
	}
	// Engine services select the protected runtime by binding, independently of
	// the historical frontend. Changing code never replaces that supervisor.
	if recovery.Release.Managed.EngineUnit.Hash != "sha256:"+view.Runtime.UnitSHA256 || recovery.SourceRelease.Managed.EngineUnit != recovery.Release.Managed.EngineUnit {
		return errEngineSource
	}
	if recovery.Release.Managed.Frontend == nil && recovery.SourceRelease.Managed.Frontend == nil {
		return ctx.Err()
	}
	manager, err := quadlet.NewManager(r.home, quadletValidator{})
	if err != nil {
		return err
	}
	defer func() { _ = manager.Close() }()
	if recovery.Release.Managed.Frontend == nil {
		unit := recovery.SourceRelease.Managed.Frontend.Unit
		if err = manager.RemoveRetained(ctx, unit.Name, unit.Hash); err != nil {
			return err
		}
	} else if err = r.installNativeRestoreFrontend(ctx, manager, recovery); err != nil {
		return err
	}
	if _, err = r.state.ReadNativeRestoreTransition(ctx, capability); err != nil {
		return err
	}
	if err = verifyNativeRestoreRecoveryUnits(ctx, r.home, recovery); err != nil {
		return err
	}
	result, err := r.session.Execute(ctx, "/usr/bin/systemctl", []string{"--user", "daemon-reload"}, nil, true)
	if err != nil || result.ExitCode != 0 || result.Truncated {
		return errEngineSource
	}
	return r.nativeRestoreQuiescent(ctx, recovery.App, view.Runtime, tree)
}

func (r nativeBackupRuntime) installNativeRestoreFrontend(ctx context.Context, manager *quadlet.Manager, recovery store.NativeRestoreRecoveryCode) error {
	if recovery.Plan.Image.ManifestDigest.Value == nil {
		return errEngineSource
	}
	client := podman.New(r.session)
	image, err := podman.ParseImage(string(recovery.Desired.Image))
	if err != nil {
		return errEngineSource
	}
	present, err := client.ImageExists(ctx, image)
	if err != nil {
		return err
	}
	if !present {
		if err = client.Pull(ctx, image); err != nil {
			return err
		}
	}
	actual, err := client.Inspect(ctx, image)
	if err != nil || actual.IndexDigest != recovery.Plan.Image.Digest || actual.ManifestDigest != *recovery.Plan.Image.ManifestDigest.Value || actual.Platform.OS != recovery.Plan.Image.Platform.OS || actual.Platform.Architecture != recovery.Plan.Image.Platform.Arch {
		return errEngineSource
	}
	for _, secret := range recovery.Release.Secrets {
		name, err := podman.ParseSecretName(secret.VersionName)
		if err != nil {
			return errEngineSource
		}
		present, err := client.SecretExists(ctx, name)
		if err != nil || !present {
			return errEngineSource
		}
	}
	unit, err := quadlet.Render(recovery.Desired, recovery.Plan, *recovery.Plan.Image.ManifestDigest.Value)
	if err != nil || unit.Name() != recovery.Release.Managed.Frontend.Unit.Name || unit.Hash() != recovery.Release.Managed.Frontend.Unit.Hash {
		return errEngineSource
	}
	if err = manager.Stage(ctx, unit); err != nil {
		return err
	}
	previous := ""
	if recovery.SourceRelease.Managed.Frontend != nil {
		previous = recovery.SourceRelease.Managed.Frontend.Unit.Hash
	}
	return manager.Install(ctx, unit, previous)
}
