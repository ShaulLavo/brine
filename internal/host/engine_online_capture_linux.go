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
	"slices"
	"strings"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

// This census contains metadata only. It is not C's stopped content seal and
// cannot establish a framed source or checkpoint creation provenance.
type engineOnlineCensus struct {
	root    engineTreeMember
	members []engineTreeMember
}

type engineOnlineDirectory struct {
	name  string
	depth int
}

type engineOnlineScanner struct {
	root   *os.Root
	limits engineCopyLimits
	census engineOnlineCensus
	queue  []engineOnlineDirectory
}

func scanEngineOnlineMetadata(ctx context.Context, root *os.Root, limits engineCopyLimits) (engineOnlineCensus, error) {
	if root == nil || !limits.valid() || ctx.Err() != nil {
		return engineOnlineCensus{}, errEngineSource
	}
	file, err := engineOpenMember(root, ".", unix.O_RDONLY, 0)
	if err != nil {
		return engineOnlineCensus{}, err
	}
	defer func() { _ = file.Close() }()
	member, err := engineSourceMember(file, ".")
	if err != nil || member.Mode&unix.S_IFMT != unix.S_IFDIR {
		return engineOnlineCensus{}, errEngineSource
	}
	scan := engineOnlineScanner{root: root, limits: limits, census: engineOnlineCensus{root: member}, queue: []engineOnlineDirectory{{}}}
	for len(scan.queue) > 0 {
		last := len(scan.queue) - 1
		directory := scan.queue[last]
		scan.queue = scan.queue[:last]
		if err := scan.directory(ctx, directory); err != nil {
			return engineOnlineCensus{}, err
		}
	}
	if !engineOwnedAliasCensus(scan.census.members) {
		return engineOnlineCensus{}, errEngineSource
	}
	slices.SortFunc(scan.census.members, func(a, b engineTreeMember) int { return strings.Compare(a.Name, b.Name) })
	return scan.census, nil
}

func (s *engineOnlineScanner) directory(ctx context.Context, directory engineOnlineDirectory) (err error) {
	name := directory.name
	if name == "" {
		name = "."
	}
	file, err := engineOpenMember(s.root, name, unix.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	for {
		entries, readErr := file.ReadDir(64)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return errEngineSource
		}
		if err := s.entries(ctx, directory, entries); err != nil {
			return err
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
	}
}

func (s *engineOnlineScanner) entries(ctx context.Context, directory engineOnlineDirectory, entries []os.DirEntry) error {
	for _, entry := range entries {
		if ctx.Err() != nil || len(s.census.members) >= s.limits.Files || directory.depth >= s.limits.Depth {
			return errEngineSource
		}
		name := entry.Name()
		if !utf8.ValidString(name) || name == "." || name == ".." || len(name) > 255 || strings.ContainsAny(name, "/\\\x00\r\n") {
			return errEngineSource
		}
		if err := s.member(filepath.Join(directory.name, name), directory.depth+1); err != nil {
			return err
		}
	}
	return nil
}

func (s *engineOnlineScanner) member(name string, depth int) (err error) {
	if len(name) > 4096 {
		return errEngineSource
	}
	file, err := engineOpenMember(s.root, name, unix.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	member, err := engineSourceMember(file, name)
	if err != nil || member.Device != s.census.root.Device || member.MountID != s.census.root.MountID {
		return errEngineSource
	}
	s.census.members = append(s.census.members, member)
	if member.Mode&unix.S_IFMT == unix.S_IFDIR {
		s.queue = append(s.queue, engineOnlineDirectory{name: name, depth: depth})
	}
	return nil
}

func enginePrefixMetadata(actual, original engineTreeMember, boundary int64) bool {
	if boundary <= 0 || original.Mode&unix.S_IFMT != unix.S_IFREG || original.Links != 1 || boundary > original.Size || actual.Size < boundary {
		return false
	}
	actual.Size = original.Size
	return actual == original
}

func engineOnlineCensusMatches(before, after engineOnlineCensus, candidate string, boundary int64) bool {
	if before.root != after.root || len(before.members) != len(after.members) {
		return false
	}
	found := candidate == ""
	for i, original := range before.members {
		current := after.members[i]
		if original.Name != candidate && current != original {
			return false
		}
		if original.Name == candidate && !enginePrefixMetadata(current, original, boundary) {
			return false
		}
		found = found || original.Name == candidate
	}
	return found
}

func hashEngineCapturedPrefix(ctx context.Context, file *os.File, original engineTreeMember, boundary int64) (string, error) {
	if file == nil || ctx.Err() != nil {
		return "", errEngineSource
	}
	before, err := engineSourceMember(file, original.Name)
	if err != nil || !enginePrefixMetadata(before, original, boundary) {
		return "", errEngineSource
	}
	hash := sha256.New()
	reader := &engineBoundedReader{ctx: ctx, reader: io.NewSectionReader(file, 0, boundary)}
	written, err := io.CopyBuffer(hash, reader, make([]byte, 64<<10))
	if err != nil || written != boundary || ctx.Err() != nil {
		return "", errEngineSource
	}
	after, err := engineSourceMember(file, original.Name)
	if err != nil || !enginePrefixMetadata(after, original, boundary) || after.Size < before.Size {
		return "", errEngineSource
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func checkEngineCapturedPrefix(ctx context.Context, root *os.Root, descriptor *os.File, original engineTreeMember, boundary int64, expected string) error {
	if root == nil || len(expected) != 64 {
		return errEngineSource
	}
	hash, err := hashEngineCapturedPrefix(ctx, descriptor, original, boundary)
	if err != nil || hash != expected {
		return errEngineSource
	}
	fresh, err := engineOpenMember(root, original.Name, unix.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer func() { _ = fresh.Close() }()
	hash, err = hashEngineCapturedPrefix(ctx, fresh, original, boundary)
	if err != nil || hash != expected {
		return errEngineSource
	}
	return nil
}

func copyEngineCapturedPrefix(ctx context.Context, source *os.File, original engineTreeMember, boundary int64, destination *os.Root, name string) (copied engineTreeMember, err error) {
	if destination == nil || source == nil || ctx.Err() != nil {
		return engineTreeMember{}, errEngineSource
	}
	before, err := engineSourceMember(source, original.Name)
	if err != nil || !enginePrefixMetadata(before, original, boundary) {
		return engineTreeMember{}, errEngineSource
	}
	out, err := engineOpenMember(destination, name, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY, 0600)
	if err != nil {
		return engineTreeMember{}, err
	}
	defer func() { err = errors.Join(err, out.Close()) }()
	hash := sha256.New()
	reader := &engineBoundedReader{ctx: ctx, reader: io.NewSectionReader(source, 0, boundary)}
	written, err := io.CopyBuffer(io.MultiWriter(out, hash), reader, make([]byte, 64<<10))
	if err != nil || written != boundary || out.Sync() != nil || ctx.Err() != nil {
		return engineTreeMember{}, errEngineSource
	}
	expected := hex.EncodeToString(hash.Sum(nil))
	current, err := hashEngineCapturedPrefix(ctx, source, original, boundary)
	if err != nil || current != expected {
		return engineTreeMember{}, errEngineSource
	}
	copied, err = engineMember(out, name)
	if err != nil || copied.Size != boundary {
		return engineTreeMember{}, errEngineSource
	}
	copied.SHA256 = expected
	return copied, nil
}
