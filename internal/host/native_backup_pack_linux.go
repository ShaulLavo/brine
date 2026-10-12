//go:build linux

package host

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/restic"
	"github.com/ShaulLavo/brine/internal/store"
	"golang.org/x/sys/unix"
	"strconv"
)

func materializeNativeBundle(ctx context.Context, original *os.Root, source nativecapture.Source, parent *os.Root, path, pointID, stateRoot string, permit store.EnginePermit) (restic.Bundle, error) {
	if original == nil || parent == nil || source.SHA256() == "" {
		return restic.Bundle{}, errEngineSource
	}
	if err := parent.Mkdir("bundle", 0700); err != nil {
		return restic.Bundle{}, errEngineSource
	}
	target, err := parent.OpenRoot("bundle")
	if err != nil {
		return restic.Bundle{}, errEngineSource
	}
	defer func() { _ = target.Close() }()
	checkpoint, selection, initialized := source.Initialized()
	var members []nativecapture.CheckpointMember
	var identity restic.Identity
	if initialized {
		c := checkpoint.Record()
		members = c.Members
		identity = restic.Identity{BindingID: c.Scope.BindingID, EpochID: c.Scope.EpochID, CaptureID: selection.Scope().CaptureID, ModuleSHA256: c.ArtifactSHA256, CheckpointSHA256: checkpoint.SHA256()}
	} else if empty, valid := source.Empty(); valid {
		members = empty.Members
		identity = restic.Identity{BindingID: empty.Scope.BindingID, EpochID: empty.Scope.EpochID, CaptureID: pointID, ModuleSHA256: source.SHA256(), CheckpointSHA256: source.SHA256()}
	} else {
		return restic.Bundle{}, errEngineSource
	}
	for _, member := range members {
		if member.Path == "recovery/signing.key" {
			if err := packNativeSigningKey(ctx, target, stateRoot, permit, member); err != nil {
				return restic.Bundle{}, err
			}
			continue
		}
		if err := copyNativeBundleMember(ctx, original, target, member); err != nil {
			return restic.Bundle{}, err
		}
	}
	for _, member := range selection.Members() {
		if err := copyNativeBundleMember(ctx, original, target, nativecapture.CheckpointMember{Path: member.Path, Bytes: member.Bytes, SHA256: member.SHA256}); err != nil {
			return restic.Bundle{}, err
		}
	}
	raw, err := json.Marshal(source)
	if err != nil || target.WriteFile("native-source.json", raw, 0600) != nil || syncEngineDirectory(parent, "bundle") != nil {
		return restic.Bundle{}, errEngineSource
	}
	return restic.Seal(ctx, filepath.Join(path, "bundle"), path, identity, restic.DefaultLimits())
}

func copyNativeBundleMember(ctx context.Context, source, target *os.Root, recorded nativecapture.CheckpointMember) error {
	if recorded.Bytes > 32<<20 {
		return errEngineSource
	}
	file, err := engineOpenMember(source, recorded.Path, unix.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	member, err := engineMember(file, recorded.Path)
	if err != nil || member.Size < 0 || uint64(member.Size) != recorded.Bytes {
		return errEngineSource
	}
	member.SHA256 = recorded.SHA256
	if err := nativeBundleDirectories(target, filepath.Dir(recorded.Path)); err != nil {
		return err
	}
	return copyEngineFile(ctx, source, target, member)
}

func nativeBundleDirectories(root *os.Root, name string) error {
	if name == "." {
		return nil
	}
	return engineMakePrivateDirectories(root, name)
}

func packNativeSigningKey(ctx context.Context, target *os.Root, stateRoot string, permit store.EnginePermit, member nativecapture.CheckpointMember) error {
	key := permit.Runtime.Secrets.SigningPrivate
	if member.SHA256 != key.SHA256 {
		return errEngineSource
	}
	root, err := engineOpenRoot(stateRoot)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	raw, err := readEnginePrivateFile(ctx, root, filepath.Join("engine-secrets", string(permit.Runtime.BindingID), "signing-private", "v"+strconv.FormatUint(key.Version, 10)+".secret"), key.SHA256, 32<<10)
	defer clear(raw)
	if err != nil || uint64(len(raw)) != member.Bytes {
		return errEngineSource
	}
	if err := engineMakePrivateDirectories(target, "recovery"); err != nil {
		return err
	}
	if err := engineWritePrivateVersion(ctx, target, member.Path, key.SHA256, raw); err != nil {
		return err
	}
	return nil
}
