package restic

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type Bundle struct {
	path     string
	manifest Manifest
	sha256   string
	size     int64
	info     os.FileInfo
}

func (b Bundle) Manifest() Manifest { return b.manifest }
func (b Bundle) Close() error {
	if err := os.Remove(b.path); err != nil {
		return ErrFailed
	}
	return nil
}

// Seal reads a private staged tree. The caller, not this codec, proves that the
// staging capture represents the admitted native source and holds its locks.
func Seal(ctx context.Context, source, workspace string, identity Identity, limits Limits) (bundle Bundle, err error) {
	if !limits.valid() || !identity.valid() || source == workspace {
		return Bundle{}, ErrInvalid
	}
	if err := privateDirectory(source); err != nil {
		return Bundle{}, err
	}
	if err := privateDirectory(workspace); err != nil {
		return Bundle{}, err
	}
	if err := separateWorkspace(source, workspace); err != nil {
		return Bundle{}, err
	}
	root, err := os.OpenRoot(source)
	if err != nil {
		return Bundle{}, ErrInvalid
	}
	defer func() { _ = root.Close() }()
	rootInfo, err := root.Stat(".")
	if err != nil {
		return Bundle{}, ErrInvalid
	}
	members, err := readMembers(ctx, root, rootInfo, limits)
	if err != nil {
		return Bundle{}, err
	}
	manifest, err := newManifest(identity, members, limits)
	if err != nil {
		return Bundle{}, err
	}
	file, err := os.CreateTemp(workspace, "sealed-*.tar")
	if err != nil {
		return Bundle{}, ErrFailed
	}
	defer func() { _ = file.Close() }()
	defer func() {
		if err != nil {
			_ = os.Remove(file.Name())
		}
	}()
	if err = writeEnvelope(ctx, file, root, rootInfo, manifest, limits); err != nil {
		return Bundle{}, err
	}
	if err = verifyMemberSet(ctx, root, rootInfo, manifest, limits); err != nil {
		return Bundle{}, err
	}
	if err = file.Sync(); err != nil {
		return Bundle{}, ErrFailed
	}
	info, err := file.Stat()
	if err != nil {
		return Bundle{}, ErrFailed
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return Bundle{}, ErrFailed
	}
	hash, err := hashFile(ctx, file, info.Size())
	if err != nil {
		return Bundle{}, err
	}
	return Bundle{path: file.Name(), manifest: manifest, sha256: hash, size: info.Size(), info: info}, nil
}

func separateWorkspace(source, workspace string) error {
	relative, err := filepath.Rel(source, workspace)
	if err != nil || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return ErrInvalid
	}
	sourceInfo, err := os.Lstat(source)
	if err != nil {
		return ErrInvalid
	}
	workInfo, err := os.Lstat(workspace)
	if err != nil || os.SameFile(sourceInfo, workInfo) {
		return ErrInvalid
	}
	return nil
}

func verifyMemberSet(ctx context.Context, root *os.Root, rootInfo os.FileInfo, expected Manifest, limits Limits) error {
	members, err := readMembers(ctx, root, rootInfo, limits)
	if err != nil {
		return err
	}
	fresh, err := newManifest(expected.document.Identity, members, limits)
	if err != nil || !slices.Equal(fresh.document.Members, expected.document.Members) {
		return ErrIntegrity
	}
	return nil
}

func readMembers(ctx context.Context, root *os.Root, rootInfo os.FileInfo, limits Limits) ([]Member, error) {
	var members []Member
	pending := []string{"."}
	var total int64
	for len(pending) > 0 {
		directory := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		entries, err := scanDirectory(ctx, root, rootInfo, directory, limits)
		if err != nil {
			return nil, err
		}
		bytes, directories := directoryTotals(entries)
		total += bytes
		if total > limits.Bytes || len(members)+len(entries) > limits.Members {
			return nil, ErrLimit
		}
		members = append(members, entries...)
		pending = append(pending, directories...)
	}
	return members, nil
}

func scanDirectory(ctx context.Context, root *os.Root, rootInfo os.FileInfo, directory string, limits Limits) ([]Member, error) {
	names, err := directoryNames(root, directory, limits.Members)
	if err != nil {
		return nil, err
	}
	entries := make([]Member, 0, len(names))
	for _, name := range names {
		member, err := inspectMember(ctx, root, rootInfo, filepath.Join(directory, name), limits)
		if err != nil {
			return nil, err
		}
		entries = append(entries, member)
	}
	return entries, nil
}

func directoryTotals(entries []Member) (int64, []string) {
	var total int64
	var directories []string
	for _, member := range entries {
		total += member.Size
		if member.Type == "directory" {
			directories = append(directories, member.Path)
		}
	}
	return total, directories
}

func directoryNames(root *os.Root, name string, maximum int) ([]string, error) {
	file, err := openMember(root, name)
	if err != nil {
		return nil, ErrInvalid
	}
	defer func() { _ = file.Close() }()
	names, err := file.Readdirnames(maximum + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, ErrInvalid
	}
	if len(names) > maximum {
		return nil, ErrLimit
	}
	return names, nil
}

func inspectMember(ctx context.Context, root *os.Root, rootInfo os.FileInfo, name string, limits Limits) (Member, error) {
	if !validPath(name) {
		return Member{}, ErrInvalid
	}
	info, err := root.Lstat(name)
	if err != nil || !safeNode(info, rootInfo) {
		return Member{}, ErrInvalid
	}
	member := Member{Path: name, Mode: uint32(info.Mode().Perm()), Type: "directory"}
	if info.IsDir() {
		return member, nil
	}
	if info.Size() > limits.FileBytes {
		return Member{}, ErrLimit
	}
	file, err := openMember(root, name)
	if err != nil {
		return Member{}, ErrInvalid
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || !safeNode(opened, rootInfo) {
		return Member{}, ErrIntegrity
	}
	member.Type, member.Size = "file", info.Size()
	member.SHA256, err = hashFile(ctx, file, member.Size)
	if err != nil {
		return Member{}, err
	}
	final, err := root.Lstat(name)
	if err != nil || !os.SameFile(info, final) || final.Size() != member.Size || !safeNode(final, rootInfo) {
		return Member{}, ErrIntegrity
	}
	return member, nil
}

func hashFile(ctx context.Context, reader io.Reader, size int64) (string, error) {
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(contextReader{ctx: ctx, reader: reader}, size+1))
	if err != nil || n != size {
		return "", ErrIntegrity
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

type quotaWriter struct {
	writer    io.Writer
	remaining int64
}

func (w *quotaWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		return 0, ErrLimit
	}
	n, err := w.writer.Write(p)
	w.remaining -= int64(n)
	return n, err
}

func writeEnvelope(ctx context.Context, file *os.File, root *os.Root, rootInfo os.FileInfo, manifest Manifest, limits Limits) error {
	w := tar.NewWriter(&quotaWriter{writer: file, remaining: limits.ArchiveBytes})
	if err := writeTarBytes(w, "manifest.json", manifest.raw); err != nil {
		return err
	}
	if err := w.WriteHeader(tarHeader("data", "directory", 0)); err != nil {
		return ErrInvalid
	}
	for _, member := range manifest.document.Members {
		if err := writeTarMember(ctx, w, root, rootInfo, member, limits); err != nil {
			return err
		}
	}
	if err := w.Close(); err != nil {
		return ErrLimit
	}
	return nil
}

func tarHeader(name, kind string, size int64) *tar.Header {
	h := &tar.Header{Name: name, Mode: 0600, Typeflag: tar.TypeReg, Size: size, ModTime: time.Unix(0, 0), Format: tar.FormatUSTAR}
	if kind == "directory" {
		h.Mode, h.Typeflag = 0700, tar.TypeDir
	}
	return h
}

func writeTarBytes(w *tar.Writer, name string, raw []byte) error {
	if err := w.WriteHeader(tarHeader(name, "file", int64(len(raw)))); err != nil {
		return ErrInvalid
	}
	if _, err := w.Write(raw); err != nil {
		return ErrLimit
	}
	return nil
}

func writeTarMember(ctx context.Context, w *tar.Writer, root *os.Root, rootInfo os.FileInfo, member Member, limits Limits) error {
	current, err := inspectMember(ctx, root, rootInfo, member.Path, limits)
	if err != nil || current != member {
		return ErrIntegrity
	}
	if err := w.WriteHeader(tarHeader("data/"+member.Path, member.Type, member.Size)); err != nil {
		return ErrInvalid
	}
	if member.Type == "directory" {
		return nil
	}
	file, err := openMember(root, member.Path)
	if err != nil {
		return ErrIntegrity
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, hash), io.LimitReader(contextReader{ctx: ctx, reader: file}, member.Size+1))
	if err != nil || n != member.Size || hex.EncodeToString(hash.Sum(nil)) != member.SHA256 {
		return ErrIntegrity
	}
	return nil
}

func (b Bundle) verify(ctx context.Context) error {
	file, err := b.openVerified(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	return nil
}

func (b Bundle) openVerified(ctx context.Context) (*os.File, error) {
	info, err := os.Lstat(b.path)
	if err != nil || b.info == nil || !os.SameFile(info, b.info) || info.Size() != b.size || !safeNode(info, b.info) {
		return nil, ErrIntegrity
	}
	file, err := openArchive(b.path)
	if err != nil {
		return nil, ErrIntegrity
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(opened, b.info) || !safeNode(opened, b.info) || opened.Size() != b.size {
		_ = file.Close()
		return nil, ErrIntegrity
	}
	hash, err := hashFile(ctx, file, b.size)
	if err != nil || hash != b.sha256 {
		_ = file.Close()
		return nil, ErrIntegrity
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = file.Close()
		return nil, ErrIntegrity
	}
	return file, nil
}

func extractEnvelope(ctx context.Context, archive, directory string, manifest Manifest, limits Limits) error {
	if !limits.valid() || len(manifest.raw) == 0 {
		return ErrInvalid
	}
	if err := privateDirectory(filepath.Dir(directory)); err != nil {
		return err
	}
	if err := os.Mkdir(directory, 0700); err != nil {
		return ErrInvalid
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return ErrInvalid
	}
	defer func() { _ = root.Close() }()
	file, err := openArchive(archive)
	if err != nil {
		return ErrIntegrity
	}
	defer func() { _ = file.Close() }()
	if err := validateTarBoundary(file, manifest, limits); err != nil {
		return err
	}
	reader := tar.NewReader(contextReader{ctx: ctx, reader: io.LimitReader(file, limits.ArchiveBytes+1)})
	if err := readManifestHeader(reader, manifest); err != nil {
		return err
	}
	header, err := reader.Next()
	if err != nil || !validHeader(header, "data", "directory", 0) {
		return ErrIntegrity
	}
	for _, member := range manifest.document.Members {
		if err := extractMember(ctx, reader, root, member); err != nil {
			return err
		}
	}
	if _, err := reader.Next(); !errors.Is(err, io.EOF) {
		return ErrIntegrity
	}
	return nil
}

func envelopeSize(manifest Manifest) int64 {
	size := int64(512 + (len(manifest.raw)+511)/512*512 + 512 + 1024)
	for _, member := range manifest.document.Members {
		size += 512 + (member.Size+511)/512*512
	}
	return size
}

func validateTarBoundary(file *os.File, manifest Manifest, limits Limits) error {
	info, err := file.Stat()
	if err != nil || info.Size() != envelopeSize(manifest) || info.Size() > limits.ArchiveBytes {
		return ErrIntegrity
	}
	if _, err := file.Seek(-1024, io.SeekEnd); err != nil {
		return ErrIntegrity
	}
	trailer := make([]byte, 1024)
	if _, err := io.ReadFull(file, trailer); err != nil || !bytes.Equal(trailer, make([]byte, 1024)) {
		return ErrIntegrity
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return ErrIntegrity
	}
	return nil
}

func readManifestHeader(reader *tar.Reader, manifest Manifest) error {
	header, err := reader.Next()
	if err != nil || !validHeader(header, "manifest.json", "file", int64(len(manifest.raw))) {
		return ErrIntegrity
	}
	raw, err := io.ReadAll(io.LimitReader(reader, manifestLimit+1))
	if err != nil || !bytes.Equal(raw, manifest.raw) {
		return ErrIntegrity
	}
	return nil
}

func validHeader(h *tar.Header, name, kind string, size int64) bool {
	if h == nil {
		return false
	}
	expected := tarHeader(name, kind, size)
	return h.Name == expected.Name && h.Typeflag == expected.Typeflag && h.Mode == expected.Mode && h.Size == size && h.Format == tar.FormatUSTAR && h.Uid == 0 && h.Gid == 0 && h.Uname == "" && h.Gname == "" && h.Linkname == "" && h.Devmajor == 0 && h.Devminor == 0 && len(h.PAXRecords) == 0
}

func extractMember(ctx context.Context, reader *tar.Reader, root *os.Root, member Member) error {
	header, err := reader.Next()
	if err != nil || !validHeader(header, "data/"+member.Path, member.Type, member.Size) {
		return ErrIntegrity
	}
	if member.Type == "directory" {
		if err := root.Mkdir(member.Path, 0700); err != nil {
			return ErrFailed
		}
		return nil
	}
	file, err := root.OpenFile(member.Path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return ErrIntegrity
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(file, hash), contextReader{ctx: ctx, reader: reader})
	if err != nil || n != member.Size || hex.EncodeToString(hash.Sum(nil)) != member.SHA256 {
		return ErrIntegrity
	}
	if err := file.Sync(); err != nil {
		return ErrFailed
	}
	return nil
}
