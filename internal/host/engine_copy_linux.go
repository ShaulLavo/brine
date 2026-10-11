//go:build linux

package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/ShaulLavo/brine/internal/data"
	"golang.org/x/sys/unix"
)

type engineSourceCopy struct {
	source   *os.Root
	binding  data.SpacetimeDBBinding
	root     *os.Root
	parent   *os.Root
	name     string
	path     string
	original engineTreeSeal
	limits   engineCopyLimits
}

func copyEngineSource(ctx context.Context, binding data.SpacetimeDBBinding, staging string, name string, limits engineCopyLimits) (copy *engineSourceCopy, err error) {
	if !data.ValidID(name) || !limits.valid() || data.OverlappingPaths(binding.SourcePath(), staging) {
		return nil, errEngineSource
	}
	if _, err = data.InspectRoot(staging); err != nil {
		return nil, errEngineSource
	}
	source, err := openEngineSource(binding)
	if err != nil {
		return nil, err
	}
	parent, err := os.OpenRoot(staging)
	if err != nil {
		return nil, errors.Join(errEngineSource, source.Close())
	}
	copy = &engineSourceCopy{source: source, binding: binding, parent: parent, path: filepath.Join(staging, name), limits: limits}
	complete := false
	defer func() {
		if !complete {
			err = errors.Join(err, copy.discard())
			copy = nil
		}
	}()
	copy.original, err = sealEngineRoot(ctx, source, limits)
	if err != nil {
		return copy, err
	}
	if err = parent.Mkdir(name, 0700); err != nil {
		return copy, errEngineSource
	}
	copy.name = name
	copy.root, err = parent.OpenRoot(name)
	if err != nil {
		return copy, errEngineSource
	}
	if err = copy.members(ctx); err != nil {
		return copy, err
	}
	staged, err := sealEngineRoot(ctx, copy.root, limits)
	if err != nil || !sameEngineContents(copy.original, staged) {
		return copy, errEngineSource
	}
	if err = copy.sourceUnchanged(ctx); err != nil {
		return copy, err
	}
	if err = syncEngineDirectory(parent, name); err != nil {
		return copy, err
	}
	complete = true
	return copy, nil
}

func (c *engineSourceCopy) members(ctx context.Context) error {
	for _, member := range c.original.members {
		if ctx.Err() != nil {
			return errEngineSource
		}
		if member.Mode&unix.S_IFMT == unix.S_IFDIR {
			if err := c.root.Mkdir(member.Name, 0700); err != nil {
				return errEngineSource
			}
			continue
		}
		if err := copyEngineFile(ctx, c.source, c.root, member); err != nil {
			return err
		}
	}
	return nil
}

func copyEngineFile(ctx context.Context, source, destination *os.Root, member engineTreeMember) (err error) {
	in, err := source.Open(member.Name)
	if err != nil {
		return errEngineSource
	}
	defer func() { err = errors.Join(err, in.Close()) }()
	actual, err := engineMember(in, member.Name)
	if err != nil || actual != (engineTreeMember{Name: member.Name, Mode: member.Mode, UID: member.UID, Device: member.Device, Inode: member.Inode, MountID: member.MountID, Size: member.Size}) {
		return errEngineSource
	}
	out, err := destination.OpenFile(member.Name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return errEngineSource
	}
	defer func() { err = errors.Join(err, out.Close()) }()
	hash := sha256.New()
	written, err := io.CopyBuffer(io.MultiWriter(out, hash), &engineBoundedReader{ctx: ctx, reader: io.LimitReader(in, member.Size+1)}, make([]byte, 64<<10))
	if err != nil || written != member.Size || hex.EncodeToString(hash.Sum(nil)) != member.SHA256 || ctx.Err() != nil {
		return errEngineSource
	}
	return out.Sync()
}

type engineBoundedReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *engineBoundedReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

func sameEngineContents(source, copied engineTreeSeal) bool {
	if source.bytes != copied.bytes || len(source.members) != len(copied.members) {
		return false
	}
	for i, first := range source.members {
		second := copied.members[i]
		if first.Name != second.Name || first.Mode != second.Mode || first.UID != second.UID || first.SHA256 != second.SHA256 {
			return false
		}
		if first.Mode&unix.S_IFMT == unix.S_IFREG && first.Size != second.Size {
			return false
		}
	}
	return true
}

func (c *engineSourceCopy) sourceUnchanged(ctx context.Context) error {
	current, err := sealEngineRoot(ctx, c.source, c.limits)
	if err != nil || current.hash != c.original.hash {
		return errEngineSource
	}
	actual, err := openEngineSource(c.binding)
	if err != nil {
		return errEngineSource
	}
	current, err = sealEngineRoot(ctx, actual, c.limits)
	closeErr := actual.Close()
	if err != nil || closeErr != nil || current.hash != c.original.hash {
		return errEngineSource
	}
	return nil
}

func (c *engineSourceCopy) discard() error {
	var err error
	if c.root != nil {
		err = c.root.Close()
	}
	if c.name != "" {
		err = errors.Join(err, c.parent.RemoveAll(c.name))
	}
	err = errors.Join(err, c.source.Close(), c.parent.Close())
	return err
}

func syncEngineDirectory(root *os.Root, name string) (err error) {
	file, err := root.Open(name)
	if err != nil {
		return errEngineSource
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	return file.Sync()
}
