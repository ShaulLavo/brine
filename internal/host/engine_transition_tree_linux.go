//go:build linux

package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/store"
	"golang.org/x/sys/unix"
)

// engineTransitionTree is held only by the protected data-operation controller.
// The controller also proves unit/job/process absence before constructing it.
type engineTransitionTree struct {
	preparation *store.EnginePreparation
	lifetime    *data.EngineLock
	binding     data.SpacetimeDBBinding
	input       data.EngineTransition
	limits      engineCopyLimits
}

func (t *engineTransitionTree) active() bool {
	if t == nil || t.input.SHA256() == "" || t.binding.Validate() != nil || !t.limits.valid() {
		return false
	}
	c := t.input.Common()
	return t.preparation != nil && t.preparation.Active(t.binding.BindingID) && t.lifetime.HoldsLifetime(t.preparation.StateRoot(), t.binding.BindingID) &&
		c.Scope.BindingID == t.binding.BindingID && c.Scope.DatabaseID == t.binding.DatabaseID && c.Scope.IncarnationID == t.binding.IncarnationID
}

func (t *engineTransitionTree) relativeNames() (string, string, string) {
	parent := path.Join("transitions", t.input.Common().Scope.ID)
	return t.binding.RelativeDirectory, path.Join(parent, "staged"), path.Join(parent, "original")
}

func (t *engineTransitionTree) inspect(ctx context.Context) (data.EngineLayoutObservation, error) {
	if !t.active() || ctx.Err() != nil {
		return data.EngineLayoutObservation{}, errEngineSource
	}
	root, err := engineOpenRoot(string(t.binding.Root))
	if err != nil {
		return data.EngineLayoutObservation{}, err
	}
	defer func() { _ = root.Close() }()
	live, staged, preserved := t.relativeNames()
	names := []string{live, staged, preserved}
	locations := make([]data.EngineLocation, len(names))
	for i, name := range names {
		locations[i], err = t.inspectLocation(ctx, root, name)
		if err != nil {
			return data.EngineLayoutObservation{}, err
		}
	}
	if !t.active() || ctx.Err() != nil {
		return data.EngineLayoutObservation{}, errEngineSource
	}
	return data.EngineLayoutObservation{Live: locations[0], Staged: locations[1], Preserved: locations[2]}, nil
}

func (t *engineTransitionTree) inspectLocation(ctx context.Context, root *os.Root, relative string) (data.EngineLocation, error) {
	base, err := root.Open(".")
	if err != nil {
		return data.EngineLocation{}, errEngineSource
	}
	defer func() { _ = base.Close() }()
	rootMember, err := engineSourceMember(base, ".")
	if err != nil {
		return data.EngineLocation{}, err
	}
	parent, err := openEngineTransitionParent(root, path.Dir(relative), rootMember)
	if err != nil {
		return data.EngineLocation{}, err
	}
	defer func() { _ = parent.Close() }()
	parentMember, err := engineSourceMember(parent, "")
	if err != nil {
		return data.EngineLocation{}, err
	}
	rootID := engineTransitionDirectory(rootMember)
	parentID := engineTransitionDirectory(parentMember)
	file, err := engineOpenMember(root, relative, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if errors.Is(err, unix.ENOENT) {
		return data.AbsentEngineLocation(rootID, parentID)
	}
	if err != nil {
		return data.EngineLocation{}, err
	}
	defer func() { _ = file.Close() }()
	leaf, err := os.OpenRoot("/proc/self/fd/" + strconv.Itoa(int(file.Fd())))
	if err != nil {
		return data.EngineLocation{}, err
	}
	defer func() { _ = leaf.Close() }()
	sealed, err := sealOwnedEngineSource(ctx, leaf, t.limits)
	if err != nil || sealed.root.MountID != rootMember.MountID || sealed.root.Device != rootMember.Device {
		return data.EngineLocation{}, errEngineSource
	}
	return data.PresentEngineLocation(data.EngineTreeSeal{Root: rootID, Parent: parentID, Directory: engineTransitionDirectory(sealed.root), ContentsSHA256: engineTransitionSealDigest(sealed)})
}

func engineTransitionSealDigest(sealed engineTreeSeal) string {
	root := sealed.root
	root.Size = 0
	members := slices.Clone(sealed.members)
	for i := range members {
		if members[i].Mode&unix.S_IFMT == unix.S_IFDIR {
			members[i].Size = 0
		}
	}
	raw, _ := json.Marshal(struct {
		Version string             `json:"version"`
		Root    engineTreeMember   `json:"root"`
		Members []engineTreeMember `json:"members"`
	}{"brine-engine-transition-tree-v1", root, members})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Source seals use the stable transition digest when available, while retaining
// the legacy member hash for points created before that digest was introduced.
func engineSourceSealDigest(sealed engineTreeSeal) string {
	return engineTransitionSealDigest(sealed)
}

func engineSourceSealMatches(sealed engineTreeSeal, expected string) bool {
	return engineSourceSealDigest(sealed) == expected || sealed.hash == expected
}

func engineTransitionDirectory(member engineTreeMember) data.EngineDirectoryIdentity {
	return data.EngineDirectoryIdentity{Device: member.Device, Inode: member.Inode}
}

func openEngineTransitionParent(root *os.Root, relative string, expectedRoot engineTreeMember) (*os.File, error) {
	current := ""
	for _, part := range strings.Split(relative, "/") {
		current = path.Join(current, part)
		file, err := engineOpenMember(root, current, unix.O_RDONLY|unix.O_DIRECTORY, 0)
		if err != nil {
			return nil, err
		}
		member, err := engineSourceMember(file, "")
		_ = file.Close()
		if err != nil || member.MountID != expectedRoot.MountID || member.Device != expectedRoot.Device {
			return nil, errEngineSource
		}
	}
	return engineOpenMember(root, relative, unix.O_RDONLY|unix.O_DIRECTORY, 0)
}
