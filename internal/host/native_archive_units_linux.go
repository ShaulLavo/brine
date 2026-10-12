//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/target"
)

type nativeArchiveUnitFacts struct {
	engine   target.Unit
	frontend *target.Unit
}

func (h archiveHost) nativeArchiveOwnedUnits(ctx context.Context, c archive.Candidate) (nativeArchiveUnitFacts, error) {
	var zero nativeArchiveUnitFacts
	if !nativeArchiveCandidate(c) {
		return zero, archive.ErrRefused
	}
	d, _ := c.Databases[0].Native()
	release, err := h.service.Store.CurrentRelease(ctx, c.App)
	if c.ReleaseID == "" {
		if !errors.Is(err, store.ErrNotFound) || d.State != data.EngineUnpublished {
			return zero, archive.ErrRefused
		}
		head, runtime, err := h.service.Store.ReadEngineHead(ctx, d.Binding.BindingID)
		if err != nil || head != d.Head || runtime.State != data.EngineUnpublished || readNativeArchiveHead(runtime, head) != nil {
			return zero, archive.ErrRefused
		}
		if err = h.nativeArchiveUnpublishedFrontendAbsent(ctx, c.App); err != nil {
			return zero, err
		}
		return nativeArchiveUnitFacts{engine: target.Unit{Name: engineContainerName(d.Binding.BindingID) + ".service", Hash: "sha256:" + runtime.UnitSHA256}}, nil
	}
	if err != nil || release.ID != c.ReleaseID || release.Managed == nil || release.Managed.BindingID != d.Binding.BindingID {
		return zero, archive.ErrRefused
	}
	facts := nativeArchiveUnitFacts{engine: release.Managed.EngineUnit}
	if release.Managed.Frontend != nil {
		front := release.Managed.Frontend.Unit
		facts.frontend = &front
	}
	return facts, nil
}

func (h archiveHost) nativeArchiveUnpublishedFrontendAbsent(ctx context.Context, app string) error {
	root, err := engineOpenRoot(h.home)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	for _, parent := range []string{".config", ".config/containers", ".config/containers/systemd"} {
		info, err := root.Lstat(parent)
		if errors.Is(err, os.ErrNotExist) {
			return ctx.Err()
		}
		if err != nil || !managedAncestorNode(info, false) {
			return archive.ErrRefused
		}
	}
	if _, err = root.Lstat(filepath.Join(".config/containers/systemd", app+".container")); !errors.Is(err, os.ErrNotExist) {
		return archive.ErrRefused
	}
	return ctx.Err()
}
