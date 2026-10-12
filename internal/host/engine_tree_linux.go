//go:build linux

package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ShaulLavo/brine/internal/data"
	"golang.org/x/sys/unix"
)

type engineCopyLimits struct {
	Bytes int64
	Files int
	Depth int
}

func (l engineCopyLimits) valid() bool {
	return l.Bytes > 0 && l.Bytes <= 1<<30 && l.Files > 0 && l.Files <= 16384 && l.Depth > 0 && l.Depth <= 64
}

type engineTreeMember struct {
	Name    string `json:"name"`
	Mode    uint16 `json:"mode"`
	UID     uint32 `json:"uid"`
	Device  uint64 `json:"device"`
	Inode   uint64 `json:"inode"`
	Links   uint32 `json:"links"`
	MountID uint64 `json:"mount_id"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
}

type engineTreeSeal struct {
	root    engineTreeMember
	members []engineTreeMember
	bytes   int64
	hash    string
}

type engineTreeWalker struct {
	root        *os.Root
	limits      engineCopyLimits
	seal        engineTreeSeal
	ownedSource bool
}

func sealEngineRoot(ctx context.Context, root *os.Root, limits engineCopyLimits) (engineTreeSeal, error) {
	return sealEngineTree(ctx, root, limits, false)
}

func sealOwnedEngineSource(ctx context.Context, root *os.Root, limits engineCopyLimits) (engineTreeSeal, error) {
	return sealEngineTree(ctx, root, limits, true)
}

func sealEngineTree(ctx context.Context, root *os.Root, limits engineCopyLimits, ownedSource bool) (engineTreeSeal, error) {
	if !limits.valid() || ctx.Err() != nil {
		return engineTreeSeal{}, errEngineSource
	}
	file, err := root.Open(".")
	if err != nil {
		return engineTreeSeal{}, errEngineSource
	}
	defer func() { _ = file.Close() }()
	info, err := engineMember(file, ".")
	if err != nil || info.Mode&unix.S_IFMT != unix.S_IFDIR {
		return engineTreeSeal{}, errEngineSource
	}
	w := engineTreeWalker{root: root, limits: limits, ownedSource: ownedSource, seal: engineTreeSeal{root: info, members: []engineTreeMember{}}}
	if err = w.directory(ctx, file, "", 0); err != nil {
		return engineTreeSeal{}, err
	}
	if ownedSource && !engineOwnedAliasCensus(w.seal.members) {
		return engineTreeSeal{}, errEngineSource
	}
	slices.SortFunc(w.seal.members, func(a, b engineTreeMember) int { return strings.Compare(a.Name, b.Name) })
	raw, err := json.Marshal(struct {
		Version string             `json:"version"`
		Root    engineTreeMember   `json:"root"`
		Members []engineTreeMember `json:"members"`
	}{"brine-engine-tree-v2", info, w.seal.members})
	if err != nil || ctx.Err() != nil {
		return engineTreeSeal{}, errEngineSource
	}
	sum := sha256.Sum256(raw)
	w.seal.hash = hex.EncodeToString(sum[:])
	return w.seal, nil
}

func (w *engineTreeWalker) directory(ctx context.Context, file *os.File, prefix string, depth int) error {
	for {
		entries, err := file.ReadDir(64)
		if err != nil && !errors.Is(err, io.EOF) {
			return errEngineSource
		}
		if walkErr := w.entries(ctx, entries, prefix, depth); walkErr != nil {
			return walkErr
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
	}
}

func (w *engineTreeWalker) entries(ctx context.Context, entries []os.DirEntry, prefix string, depth int) error {
	for _, entry := range entries {
		name := entry.Name()
		if !utf8.ValidString(name) || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00\r\n") || len(name) > 255 {
			return errEngineSource
		}
		if err := w.member(ctx, filepath.Join(prefix, name), depth+1); err != nil {
			return err
		}
	}
	return nil
}

func (w *engineTreeWalker) member(ctx context.Context, name string, depth int) error {
	if ctx.Err() != nil || depth > w.limits.Depth || len(w.seal.members) >= w.limits.Files || len(name) > 4096 {
		return errEngineSource
	}
	info, err := w.root.Lstat(name)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() && !info.Mode().IsRegular() {
		return errEngineSource
	}
	file, err := engineOpenMember(w.root, name, unix.O_RDONLY, 0)
	if err != nil {
		return errEngineSource
	}
	defer func() { _ = file.Close() }()
	member, err := engineMember(file, name)
	if w.ownedSource {
		member, err = engineSourceMember(file, name)
	}
	if err != nil || member.MountID != w.seal.root.MountID || member.Device != w.seal.root.Device {
		return errEngineSource
	}
	if member.Mode&unix.S_IFMT == unix.S_IFDIR {
		w.seal.members = append(w.seal.members, member)
		return w.directory(ctx, file, name, depth)
	}
	if member.Size < 0 || member.Size > w.limits.Bytes-w.seal.bytes {
		return errEngineSource
	}
	member.SHA256, err = hashEngineFile(ctx, file, member)
	if err != nil {
		return err
	}
	w.seal.bytes += member.Size
	w.seal.members = append(w.seal.members, member)
	return nil
}

func engineMember(file *os.File, name string) (engineTreeMember, error) {
	member, err := engineSourceMember(file, name)
	if err != nil || member.Mode&unix.S_IFMT == unix.S_IFREG && member.Links != 1 {
		return engineTreeMember{}, errEngineSource
	}
	return member, nil
}

func engineSourceMember(file *os.File, name string) (engineTreeMember, error) {
	var stat unix.Statx_t
	mask := unix.STATX_BASIC_STATS | unix.STATX_MNT_ID
	if unix.Statx(int(file.Fd()), "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW, mask, &stat) != nil || stat.Mask&unix.STATX_MNT_ID == 0 || stat.Mnt_id == 0 || int64(stat.Uid) != int64(os.Geteuid()) {
		return engineTreeMember{}, errEngineSource
	}
	mode := stat.Mode
	validDirectory := mode&unix.S_IFMT == unix.S_IFDIR && mode&07777 == 0700
	validFile := mode&unix.S_IFMT == unix.S_IFREG && mode&07777 == 0600 && stat.Nlink >= 1
	if !validDirectory && !validFile || stat.Size > 1<<30 {
		return engineTreeMember{}, errEngineSource
	}
	return engineTreeMember{Name: name, Mode: mode, UID: stat.Uid, Device: unix.Mkdev(stat.Dev_major, stat.Dev_minor), Inode: stat.Ino, Links: stat.Nlink, MountID: stat.Mnt_id, Size: int64(stat.Size)}, nil
}

func engineOwnedAliasCensus(members []engineTreeMember) bool {
	type identity struct{ device, inode, mount uint64 }
	counts := map[identity]uint32{}
	values := map[identity]engineTreeMember{}
	for _, member := range members {
		if member.Mode&unix.S_IFMT != unix.S_IFREG {
			continue
		}
		id := identity{member.Device, member.Inode, member.MountID}
		member.Name = ""
		previous, found := values[id]
		if found && member != previous {
			return false
		}
		values[id] = member
		counts[id]++
	}
	for id, member := range values {
		if counts[id] != member.Links {
			return false
		}
	}
	return true
}

func hashEngineFile(ctx context.Context, file *os.File, expected engineTreeMember) (string, error) {
	before, err := engineSourceMember(file, "")
	expected.Name = ""
	if err != nil || before != expected {
		return "", errEngineSource
	}
	hash := sha256.New()
	buffer := make([]byte, 64<<10)
	var read int64
	for {
		if ctx.Err() != nil {
			return "", errEngineSource
		}
		n, err := file.Read(buffer)
		read += int64(n)
		if read > expected.Size {
			return "", errEngineSource
		}
		_, _ = hash.Write(buffer[:n])
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", errEngineSource
		}
	}
	after, err := engineSourceMember(file, "")
	if err != nil || before != after || read != expected.Size || ctx.Err() != nil {
		return "", errEngineSource
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func openEngineSource(binding data.SpacetimeDBBinding) (*os.Root, error) {
	if binding.Validate() != nil {
		return nil, errEngineSource
	}
	if _, err := data.InspectRoot(string(binding.Root)); err != nil {
		return nil, errEngineSource
	}
	root, err := engineOpenRoot(string(binding.Root))
	if err != nil {
		return nil, errEngineSource
	}
	defer func() { _ = root.Close() }()
	if err = engineDirectoryChain(root, binding.RelativeDirectory); err != nil {
		return nil, err
	}
	file, err := engineOpenMember(root, binding.RelativeDirectory, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, errEngineSource
	}
	defer func() { _ = file.Close() }()
	return os.OpenRoot("/proc/self/fd/" + strconv.FormatUint(uint64(file.Fd()), 10))
}

func engineDirectoryChain(root *os.Root, relative string) error {
	base, err := root.Open(".")
	if err != nil {
		return errEngineSource
	}
	identity, err := engineMember(base, ".")
	closeErr := base.Close()
	if err != nil || closeErr != nil {
		return errEngineSource
	}
	prefix := ""
	for _, part := range strings.Split(relative, "/") {
		prefix = filepath.Join(prefix, part)
		info, err := root.Lstat(prefix)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
			return errEngineSource
		}
		if err = sameEngineMount(root, prefix, identity); err != nil {
			return err
		}
	}
	return nil
}

func sameEngineMount(root *os.Root, relative string, identity engineTreeMember) error {
	file, err := engineOpenMember(root, relative, unix.O_RDONLY, 0)
	if err != nil {
		return errEngineSource
	}
	member, err := engineMember(file, relative)
	closeErr := file.Close()
	if err != nil || closeErr != nil || member.MountID != identity.MountID || member.Device != identity.Device {
		return errEngineSource
	}
	return nil
}
