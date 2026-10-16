//go:build linux

package workspaceobserve

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/pelletier/go-toml/v2"
	"golang.org/x/sys/unix"
)

const GrantPath = "/etc/ssh/brine/workspace-observer.toml"
const policyPath = "/etc/ssh/brine/operator-policy.toml"
const enrollmentPath = "/var/lib/brine-enrollment/record.json"
const DefaultGrant = "allow_workspace_observation = false\n"

type scope struct {
	uid, gid   uint32
	roots      []string
	policyHash string
}

func readProtected(path string, limit int64) ([]byte, error) {
	if path != GrantPath && path != policyPath && path != enrollmentPath {
		return nil, ErrUnknown
	}
	for current := filepath.Dir(path); ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode().Perm()&0022 != 0 {
			return nil, ErrUnknown
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 {
			return nil, ErrUnknown
		}
		if current == "/" {
			break
		}
	}
	how := unix.OpenHow{Flags: unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS}
	fd, err := unix.Openat2(unix.AT_FDCWD, path, &how)
	if err != nil {
		return nil, ErrUnknown
	}
	file := os.NewFile(uintptr(fd), path)
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !protectedReadMode(info.Mode().Perm()) || info.Size() > limit {
		return nil, ErrUnknown
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 || stat.Nlink != 1 {
		return nil, ErrUnknown
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	named, namedErr := os.Lstat(path)
	if err != nil || namedErr != nil || !os.SameFile(info, named) || int64(len(raw)) != info.Size() {
		clear(raw)
		return nil, ErrUnknown
	}
	return raw, nil
}
func enrolled(complete bool) (scope, error) {
	raw, err := readProtected(enrollmentPath, 1<<20)
	if err != nil {
		return scope{}, err
	}
	defer clear(raw)
	var record struct {
		UID     int64 `json:"uid"`
		GID     int64 `json:"gid"`
		Journal struct {
			Phase string `json:"phase"`
		} `json:"journal"`
	}
	if json.Unmarshal(raw, &record) != nil || record.UID <= 0 || record.UID > math.MaxUint32 || record.GID <= 0 || record.GID > math.MaxUint32 || complete && record.Journal.Phase != "enrolled" {
		return scope{}, ErrUnknown
	}
	return scope{uid: uint32(record.UID), gid: uint32(record.GID)}, nil
}
func loadScope() (scope, error) {
	current, err := enrolled(true)
	if err != nil {
		return scope{}, err
	}
	grant, err := readProtected(GrantPath, 4096)
	if err != nil {
		return scope{}, err
	}
	defer clear(grant)
	allowed, err := ParseGrant(grant)
	if err != nil || !allowed {
		return scope{}, ErrUnknown
	}
	raw, err := readProtected(policyPath, 1<<20)
	if err != nil {
		return scope{}, err
	}
	defer clear(raw)
	operator, err := policy.Parse(raw)
	if err != nil {
		return scope{}, ErrUnknown
	}
	current.roots, current.policyHash = operator.PersistentRoots(), operator.Hash()
	return current, nil
}
func openDirectory(path string) (*os.File, error) {
	how := unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS}
	fd, err := unix.Openat2(unix.AT_FDCWD, path, &how)
	if err != nil {
		return nil, ErrUnknown
	}
	return os.NewFile(uintptr(fd), path), nil
}
func (s scope) authorize(directory *os.File) (directoryIdentity, string, error) {
	held, uid, err := identity(directory)
	if err != nil || uid != s.uid {
		return directoryIdentity{}, "", ErrUnknown
	}
	path, err := os.Readlink("/proc/self/fd/" + strconv.FormatUint(uint64(directory.Fd()), 10))
	if err != nil || !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.HasSuffix(path, " (deleted)") {
		return directoryIdentity{}, "", ErrUnknown
	}
	for _, allowed := range s.roots {
		relative, err := filepath.Rel(allowed, path)
		if err != nil || !validObservationRelative(relative) {
			continue
		}
		if err = s.authorizedAlias(allowed, relative, held); err == nil {
			return held, path, nil
		}
	}
	return directoryIdentity{}, "", ErrUnknown
}
func (s scope) authorizedAlias(allowed, relative string, held directoryIdentity) error {
	root, err := openDirectory(allowed)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	rootIdentity, uid, err := identity(root)
	if err != nil || uid != s.uid {
		return ErrUnknown
	}
	prefix := ""
	for _, part := range strings.Split(relative, "/") {
		prefix = filepath.Join(prefix, part)
		how := unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_XDEV}
		fd, err := unix.Openat2(int(root.Fd()), prefix, &how)
		if err != nil {
			return ErrUnknown
		}
		file := os.NewFile(uintptr(fd), prefix)
		member, uid, err := identity(file)
		closeErr := file.Close()
		if err != nil || closeErr != nil || uid != s.uid || member.Mount != rootIdentity.Mount || member.Device != rootIdentity.Device {
			return ErrUnknown
		}
		if prefix == relative && (member != held) {
			return ErrUnknown
		}
	}
	return nil
}

func ParseGrant(raw []byte) (bool, error) {
	if len(raw) > 4096 {
		return false, ErrUnknown
	}
	var permission struct {
		Allow bool `toml:"allow_workspace_observation"`
	}
	if toml.NewDecoder(bytes.NewReader(raw)).DisallowUnknownFields().Decode(&permission) != nil {
		return false, ErrUnknown
	}
	return permission.Allow, nil
}

func protectedReadMode(mode os.FileMode) bool {
	return mode == 0600 || mode == 0644
}
