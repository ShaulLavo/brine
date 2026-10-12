//go:build linux

package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/quadlet"
	"github.com/ShaulLavo/brine/internal/target"
	"path/filepath"
	"slices"
	"time"
)

func nativeArchiveCandidate(c archive.Candidate) bool {
	return len(c.Databases) == 1 && c.Databases[0].Engine() == data.EngineSpacetimeDB
}

func nativeArchiveTreeLimits() engineCopyLimits {
	return engineCopyLimits{Bytes: 256 << 20, Files: 16384, Depth: 20}
}

func (h archiveHost) nativeArchiveFacts(ctx context.Context, c archive.Candidate, requireRetention bool) (archive.Plan, error) {
	var zero archive.Plan
	if !nativeArchiveCandidate(c) {
		return zero, archive.ErrRefused
	}
	d, _ := c.Databases[0].Native()
	pol, err := h.service.Policy.Load(ctx)
	if err != nil || !slices.Contains(pol.PersistentRoots(), string(d.Binding.Root)) || d.Allocation == nil || data.VerifyNativeArchiveAllocation(d.Binding, *d.Allocation) != nil {
		return zero, archive.ErrRefused
	}
	if err = h.verifyNativeArchiveUnits(ctx, c); err != nil {
		return zero, err
	}
	for _, epoch := range d.Epochs {
		destination, ok := pol.BackupDestination(epoch.Destination.Reference)
		evidence, retained := pol.BackupRetention(epoch.Destination.Reference)
		if !ok || destination != epoch.Destination || requireRetention && (!retained || !evidence.Admits(destination, time.Now().UTC())) {
			return zero, archive.ErrRefused
		}
		d.Retention = append(d.Retention, evidence)
	}
	limits := nativeArchiveTreeLimits()
	base, err := engineOpenRoot(string(d.Binding.Root))
	if err != nil {
		return zero, archive.ErrRefused
	}
	defer func() { _ = base.Close() }()
	observer := engineTransitionTree{limits: limits}
	for i := range d.TransitionTrees {
		location, err := observer.inspectLocation(ctx, base, filepath.Join("transitions", d.TransitionTrees[i].TransitionID))
		seal, present := location.Tree()
		if err != nil || !present {
			return zero, archive.ErrRefused
		}
		d.TransitionTrees[i].Seal = seal
	}
	snapshot, err := h.service.Inventory.Collect(ctx)
	if err != nil || snapshot.Generation.Status != target.KnownStatus || snapshot.Generation.Value == nil {
		return zero, archive.ErrRefused
	}
	raw, err := json.Marshal(snapshot.Identity)
	if err != nil {
		return zero, err
	}
	hash := sha256.Sum256(raw)
	c.Databases[0] = archive.NativeDatabaseVariant(d)
	return archive.Plan{Candidate: c, Generation: *snapshot.Generation.Value, PolicyHash: pol.Hash(), TargetHash: "sha256:" + hex.EncodeToString(hash[:])}, nil
}

func (h archiveHost) verifyNativeArchiveUnits(ctx context.Context, c archive.Candidate) error {
	facts, err := h.nativeArchiveOwnedUnits(ctx, c)
	d, _ := c.Databases[0].Native()
	if err != nil {
		return archive.ErrRefused
	}
	permit, err := h.service.Store.ReadEnginePermit(ctx, d.Binding.BindingID)
	if err != nil || readNativeArchiveHead(permit.Runtime, d.Head) != nil || verifyEngineService(ctx, h.home, permit) != nil {
		return archive.ErrRefused
	}
	if facts.frontend != nil {
		unit := *facts.frontend
		if quadlet.VerifyCurrent(ctx, h.home, unit.Name, unit.Hash) != nil {
			return archive.ErrRefused
		}
	}
	return ctx.Err()
}

func readNativeArchiveHead(runtime data.EngineRuntime, head data.EngineHeadRef) error {
	hash, err := managedRuntimeHash(runtime)
	if err != nil || hash != head.RuntimeSHA256 || runtime.BindingID != head.BindingID || runtime.EpochID != head.EpochID {
		return archive.ErrRefused
	}
	return nil
}
