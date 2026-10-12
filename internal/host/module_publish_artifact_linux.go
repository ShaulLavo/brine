//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/store"
	"golang.org/x/sys/unix"
)

type publicationArtifact struct {
	preparation *store.EnginePreparation
	binding     data.ReplicaBindingID
	root        *os.Root
	name        string
	nonce       string
	member      engineTreeMember
	artifact    datapublish.Artifact
}

func stagePublicationArtifact(ctx context.Context, preparation *store.EnginePreparation, binding data.ReplicaBindingID, artifact datapublish.Artifact) (staged *publicationArtifact, err error) {
	if !preparation.Active(binding) || ctx.Err() != nil || !data.ValidCatalogHash(string(artifact.WasmSHA256())) {
		return nil, datapublish.ErrRecord
	}
	root, err := engineOpenRoot(preparation.StateRoot())
	if err != nil {
		return nil, datapublish.ErrRecord
	}
	staged = &publicationArtifact{preparation: preparation, binding: binding, root: root, artifact: artifact}
	defer func() {
		if err != nil {
			err = errors.Join(err, staged.discard())
			staged = nil
		}
	}()
	if err = engineMakePrivateDirectories(root, "module-publications"); err != nil {
		return staged, datapublish.ErrRecord
	}
	staged.nonce, err = data.NewID()
	if err != nil {
		return staged, datapublish.ErrRecord
	}
	staged.name = filepath.Join("module-publications", ".inspect-"+staged.nonce+".wasm")
	if err = staged.write(ctx); err != nil {
		return staged, err
	}
	if err = staged.unchanged(ctx); err != nil {
		return staged, err
	}
	return staged, nil
}

func (a *publicationArtifact) write(ctx context.Context) (err error) {
	file, err := engineOpenMember(a.root, a.name, unix.O_CREAT|unix.O_EXCL|unix.O_RDWR, 0600)
	if err != nil {
		return datapublish.ErrRecord
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	a.member, err = engineMember(file, a.name)
	if err != nil {
		return datapublish.ErrRecord
	}
	raw := a.artifact.Bytes()
	written, err := file.Write(raw)
	if err != nil || written != len(raw) || ctx.Err() != nil || file.Sync() != nil {
		return datapublish.ErrRecord
	}
	a.member, err = engineMember(file, a.name)
	if err != nil || a.member.Size != int64(len(raw)) {
		return datapublish.ErrRecord
	}
	return syncEngineDirectory(a.root, "module-publications")
}

func (a *publicationArtifact) unchanged(ctx context.Context) error {
	if a == nil || a.root == nil || !a.preparation.Active(a.binding) || ctx.Err() != nil {
		return datapublish.ErrRecord
	}
	file, err := engineOpenMember(a.root, a.name, unix.O_RDONLY, 0)
	if err != nil {
		return datapublish.ErrRecord
	}
	defer func() { _ = file.Close() }()
	member, err := engineMember(file, a.name)
	if err != nil || member != a.member {
		return datapublish.ErrRecord
	}
	raw, err := readEnginePrivateFile(ctx, a.root, a.name, string(a.artifact.WasmSHA256()), datapublish.ModuleArtifactLimit)
	if err != nil {
		return datapublish.ErrRecord
	}
	observed, err := datapublish.NewArtifact(raw)
	if err != nil || observed.WasmSHA256() != a.artifact.WasmSHA256() || observed.ProgramWireHash() != a.artifact.ProgramWireHash() {
		return datapublish.ErrRecord
	}
	return nil
}

func (a *publicationArtifact) path() string {
	if a == nil || !a.preparation.Active(a.binding) {
		return ""
	}
	return filepath.Join(a.preparation.StateRoot(), a.name)
}

func (a *publicationArtifact) discard() error {
	if a == nil || a.root == nil {
		return nil
	}
	var err error
	if a.name != "" {
		err = a.removeOwned()
	}
	err = errors.Join(err, a.root.Close())
	a.root = nil
	return err
}

func (a *publicationArtifact) removeOwned() error {
	if _, err := a.root.Lstat(a.name); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	file, err := engineOpenMember(a.root, a.name, unix.O_RDONLY, 0)
	if err != nil {
		return datapublish.ErrRecord
	}
	defer func() { _ = file.Close() }()
	current, err := engineMember(file, a.name)
	if err != nil || current.Device != a.member.Device || current.Inode != a.member.Inode || current.MountID != a.member.MountID || current.Mode != a.member.Mode || current.UID != a.member.UID || current.Links != a.member.Links {
		return datapublish.ErrRecord
	}
	return a.root.Remove(a.name)
}
