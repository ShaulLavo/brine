//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/ShaulLavo/brine/internal/store"
	"golang.org/x/sys/unix"
)

type publicationTargetStage struct {
	state      *store.Store
	capability *store.PublicationTargetPreparation
	checkpoint publicationTargetCheckpoint
	facts      store.PublicationTargetFacts
	base       *os.Root
	parent     *os.Root
	staged     *os.Root
	parentName string
}

func preparePublicationTarget(ctx context.Context, state *store.Store, capability *store.PublicationTargetPreparation, checkpoint publicationTargetCheckpoint, home string) (_ *publicationTargetStage, err error) {
	if state == nil || checkpoint.unchanged(ctx) != nil {
		return nil, errEngineSource
	}
	facts, err := state.ReadPublicationTargetFacts(ctx, capability)
	if err != nil || facts.Checkpoint.SHA256() != checkpoint.checkpoint.SHA256() {
		return nil, errors.Join(errEngineSource, err)
	}
	stage := &publicationTargetStage{state: state, capability: capability, checkpoint: checkpoint, facts: facts, parentName: filepath.Join("transitions", facts.TransitionID)}
	complete := false
	defer func() {
		if !complete {
			err = errors.Join(err, stage.close())
		}
	}()
	stage.base, err = engineOpenRoot(string(facts.Binding.Root))
	if err != nil {
		return nil, err
	}
	if err = engineMakePrivateDirectories(stage.base, "transitions"); err != nil {
		return nil, err
	}
	if err = syncEngineDirectory(stage.base, "."); err != nil {
		return nil, err
	}
	if err = stage.base.Mkdir(stage.parentName, 0700); err != nil {
		return nil, errEngineSource
	}
	stage.parent, err = stage.base.OpenRoot(stage.parentName)
	if err != nil {
		return nil, err
	}
	if err = engineWritePrivateVersion(ctx, stage.parent, ".brine-publication-owner", nativeBackupBytesHash([]byte(facts.IntentSHA256)), []byte(facts.IntentSHA256)); err != nil {
		return nil, err
	}
	if err = syncEngineDirectory(stage.base, "transitions"); err != nil {
		return nil, err
	}
	if err = stage.parent.Mkdir("staged", 0700); err != nil {
		return nil, errEngineSource
	}
	stage.staged, err = stage.parent.OpenRoot("staged")
	if err != nil {
		return nil, err
	}
	if err = copyPublicationTarget(ctx, checkpoint, stage.staged); err != nil {
		return nil, err
	}
	if err = syncNativeRestoreTarget(ctx, stage.staged, checkpoint.stage.source.limits); err != nil {
		return nil, err
	}
	if err = syncEngineDirectory(stage.parent, "."); err != nil {
		return nil, err
	}
	unit, err := readNativeRestoreUnit(ctx, home, facts.Runtime)
	if err != nil {
		return nil, err
	}
	permit := checkpoint.stage.source.permit
	permit.Runtime = facts.Runtime.Clone()
	material, err := readEngineMaterial(ctx, checkpoint.stage.source.preparation.StateRoot(), permit)
	material.clear()
	if err != nil {
		return nil, err
	}
	observed, err := stage.observe(ctx, unit)
	if err != nil || checkpoint.unchanged(ctx) != nil {
		return nil, errors.Join(errEngineSource, err)
	}
	if err = state.RecordPublicationTarget(ctx, capability, observed); err != nil {
		return nil, err
	}
	complete = true
	return stage, nil
}

func copyPublicationTarget(ctx context.Context, checkpoint publicationTargetCheckpoint, target *os.Root) error {
	if checkpoint.unchanged(ctx) != nil {
		return errEngineSource
	}
	selected := make(map[string]bool)
	for _, member := range checkpoint.checkpoint.Record().Members {
		selected[member.Path] = true
	}
	for _, member := range checkpoint.selection.Members() {
		selected[member.Path] = true
	}
	for _, member := range checkpoint.seal.members {
		if !selected[member.Name] {
			continue
		}
		if member.Mode&unix.S_IFMT != unix.S_IFREG || engineMakePrivateDirectories(target, filepath.Dir(member.Name)) != nil {
			return errEngineSource
		}
		if err := copyEngineFile(ctx, checkpoint.stage.source.root, target, member); err != nil {
			return err
		}
		delete(selected, member.Name)
	}
	if len(selected) != 0 {
		return errEngineSource
	}
	return checkpoint.unchanged(ctx)
}

func (s *publicationTargetStage) observe(ctx context.Context, unit string) (store.PublicationTargetObservation, error) {
	zero := store.PublicationTargetObservation{}
	if s == nil || s.base == nil || s.parent == nil || s.staged == nil || s.checkpoint.unchanged(ctx) != nil {
		return zero, errEngineSource
	}
	tree := engineTransitionTree{limits: s.checkpoint.stage.source.limits}
	old, err := tree.inspectLocation(ctx, s.base, s.facts.Binding.RelativeDirectory)
	oldSeal, present := old.Tree()
	if err != nil || !present {
		return zero, errEngineSource
	}
	location, err := tree.inspectLocation(ctx, s.base, filepath.Join(s.parentName, "staged"))
	stagedSeal, present := location.Tree()
	if err != nil || !present {
		return zero, errEngineSource
	}
	a := s.checkpoint.stage.source.permit.Allocation
	if a == nil {
		return zero, errEngineSource
	}
	allocation := *a
	allocation.DirectoryDevice, allocation.DirectoryInode = stagedSeal.Directory.Device, stagedSeal.Directory.Inode
	allocation.AllocatedAt = time.Now().UTC()
	raw, _ := json.Marshal(s.facts.Runtime.Secrets)
	return store.PublicationTargetObservation{Parent: s.parent, Staged: s.staged, OldTree: oldSeal, TargetTree: stagedSeal, Allocation: allocation, UnitSHA256: unit, MaterialSHA256: nativeBackupBytesHash(raw), ObservedAt: time.Now().UTC()}, nil
}

func (s *publicationTargetStage) close() error {
	if s == nil {
		return nil
	}
	var err error
	for _, root := range []*os.Root{s.staged, s.parent, s.base} {
		if root != nil {
			err = errors.Join(err, root.Close())
		}
	}
	s.staged, s.parent, s.base = nil, nil, nil
	return err
}
