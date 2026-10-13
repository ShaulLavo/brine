//go:build linux

package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/store"
	"golang.org/x/sys/unix"
)

func WriteEngineMaterial(ctx context.Context, preparation *store.EnginePreparation, runtime data.EngineRuntime, material *EngineSecretMaterial) error {
	if !preparation.Active(runtime.BindingID) || runtime.Validate() != nil || material == nil || !engineAuthPolicy(material.values[3]) {
		return errEngineSource
	}
	versions := []data.EngineSecretVersion{runtime.Secrets.Publisher, runtime.Secrets.SigningPrivate, runtime.Secrets.SigningPublic, runtime.Secrets.AuthPolicy}
	for i, version := range versions {
		sum := sha256.Sum256(material.values[i])
		if len(material.values[i]) == 0 || hex.EncodeToString(sum[:]) != version.SHA256 {
			return errEngineSource
		}
	}
	root, err := engineOpenRoot(preparation.StateRoot())
	if err != nil {
		return errEngineSource
	}
	defer func() { _ = root.Close() }()
	for i, role := range []string{"publisher", "signing-private", "signing-public", "auth-policy"} {
		parent := filepath.Join("engine-secrets", string(runtime.BindingID), role)
		if err = engineMakePrivateDirectories(root, parent); err != nil {
			return err
		}
		name := filepath.Join(parent, "v"+strconv.FormatUint(versions[i].Version, 10)+".secret")
		if err = engineWritePrivateVersion(ctx, root, name, versions[i].SHA256, material.values[i]); err != nil {
			return err
		}
	}
	if !preparation.Active(runtime.BindingID) {
		return errEngineSource
	}
	return ctx.Err()
}

func engineMakePrivateDirectories(root *os.Root, path string) error {
	current := ""
	for _, part := range strings.Split(path, "/") {
		current = filepath.Join(current, part)
		if _, err := engineMakePrivateDirectory(root, current); err != nil {
			return err
		}
	}
	return nil
}

func engineMakePrivateDirectory(root *os.Root, path string) (bool, error) {
	err := root.Mkdir(path, 0700)
	created := err == nil
	if err != nil && !errors.Is(err, os.ErrExist) {
		return false, errEngineSource
	}
	if engineDirectoryChain(root, path) != nil {
		return created, errEngineSource
	}
	return created, nil
}

func engineWritePrivateVersion(ctx context.Context, root *os.Root, name, hash string, content []byte) error {
	if _, err := root.Lstat(name); err == nil {
		raw, err := readEnginePrivateFile(ctx, root, name, hash, 32<<10)
		clear(raw)
		return err
	} else if !errors.Is(err, os.ErrNotExist) {
		return errEngineSource
	}
	id, err := data.NewID()
	if err != nil {
		return errEngineSource
	}
	temporary := filepath.Join(filepath.Dir(name), ".brine-"+id)
	file, err := engineOpenMember(root, temporary, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY, 0600)
	if err != nil {
		return errEngineSource
	}
	defer func() { _ = file.Close(); _ = root.Remove(temporary) }()
	n, err := file.Write(content)
	if err != nil || n != len(content) || ctx.Err() != nil || file.Sync() != nil {
		return errEngineSource
	}
	base, err := root.Open(".")
	if err != nil {
		return errEngineSource
	}
	defer func() { _ = base.Close() }()
	err = unix.Renameat2(int(base.Fd()), temporary, int(base.Fd()), name, unix.RENAME_NOREPLACE)
	if err != nil && !errors.Is(err, unix.EEXIST) {
		return errEngineSource
	}
	raw, err := readEnginePrivateFile(ctx, root, name, hash, 32<<10)
	clear(raw)
	if err != nil {
		return err
	}
	return syncEngineDirectory(root, filepath.Dir(name))
}
