//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/store"
)

func (h archiveHost) replayNativeArchivedSource(ctx context.Context, p archive.Plan, d archive.NativeDatabase) (out archive.NativeRetentionReceipt, expires time.Time, err error) {
	lifetime := h.nativeLifetimes[d.Binding.BindingID]
	if !lifetime.HoldsLifetime(h.stateRoot, d.Binding.BindingID) || h.nativeArchiveMaintenanceQuiescent(ctx, p, d) != nil {
		return out, expires, archive.ErrRefused
	}
	preparation, err := h.service.Store.AcquireNativeArchivePreparation(ctx, p.ArchiveID, d.Binding.BindingID, lifetime)
	if err != nil {
		return out, expires, err
	}
	defer func() { err = errors.Join(err, preparation.Close()) }()
	capability, err := h.service.Store.NativeArchivedSource(ctx, p.ArchiveID, d.Binding.BindingID, preparation, lifetime)
	if err != nil {
		return out, expires, err
	}
	bundle, err := h.service.Store.ReadNativeArchivedBundle(ctx, capability)
	if err != nil {
		return out, expires, err
	}
	admission, err := h.service.Store.NativeArchivedRepository(ctx, capability)
	if err != nil {
		return out, expires, err
	}
	material, err := admission.Material(ctx)
	if err != nil {
		return out, expires, err
	}
	expires = material.Scope.ExpiresAt
	runtime := nativeBackupRuntime{state: h.service.Store, stateRoot: h.stateRoot, home: h.home, session: h.session}
	client, repository, err := runtime.repository(ctx, admission)
	if err != nil {
		return out, expires, err
	}
	parent, path, err := newNativeRestoreParent(ctx, string(d.Binding.Root))
	if err != nil {
		return out, expires, err
	}
	remove := true
	defer func() {
		if remove {
			err = errors.Join(err, removeNativeArchiveReplayParent(string(d.Binding.Root), path, parent))
		}
		err = errors.Join(err, parent.Close())
	}()
	extracted, err := client.RestoreExact(ctx, repository, path, parent, "download", bundle.Transport(), bundle.Manifest())
	if err != nil {
		return out, expires, err
	}
	defer func() { err = errors.Join(err, extracted.Root().Close()) }()
	observed := store.NativeArchiveVerificationObservation{Parent: parent}
	checkpoint, selection, initialized := bundle.Source().Initialized()
	if initialized {
		// Unknown private reader outcomes retain their input for inspection.
		remove = false
		evidence, disposal, readErr := runtime.verifyExtracted(ctx, extracted, parent, path, checkpoint, selection, bundle.SourceEvidence(), bundle.Schema(), bundle.ConfirmedBound(), bundle.FinalPairs())
		if readErr != nil {
			return out, expires, readErr
		}
		remove = true
		observed.Evidence, observed.ReaderNonce, observed.ReaderViewSHA256, observed.ReaderImageID, observed.ReaderDisposedAt = evidence, disposal.nonce, disposal.viewHash, disposal.imageID, disposal.at
	}
	if h.nativeArchiveMaintenanceQuiescent(ctx, p, d) != nil {
		return out, expires, archive.ErrRefused
	}
	out, err = h.service.Store.RecordNativeArchivedReplay(ctx, capability, extracted, observed)
	return out, expires, err
}

func removeNativeArchiveReplayParent(rootPath, path string, parent *os.Root) error {
	relative, err := filepath.Rel(rootPath, path)
	if err != nil || filepath.Dir(relative) != "native-restores" || !data.ValidID(filepath.Base(relative)) || parent == nil {
		return errEngineSource
	}
	base, err := engineOpenRoot(rootPath)
	if err != nil {
		return err
	}
	defer func() { _ = base.Close() }()
	held, err := parent.Stat(".")
	current, currentErr := base.Lstat(relative)
	if err != nil || currentErr != nil || !current.IsDir() || !os.SameFile(held, current) {
		return errEngineSource
	}
	if err = base.RemoveAll(relative); err != nil {
		return err
	}
	return syncEngineDirectory(base, "native-restores")
}
