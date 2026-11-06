//go:build linux

package host

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/sqliteprep"
	"github.com/ShaulLavo/brine/internal/store"
	"golang.org/x/sys/unix"
)

var errSQLiteSource = errors.New("host: SQLite source identity unavailable or changed")

// sqliteSourceSuffixes are the bounded SQLite member names. SHM is SQLite's
// rebuildable WAL index, not recovered application data.
var sqliteSourceSuffixes = [...]string{"", "-wal", "-shm", "-journal"}

// sqliteSource retains the persistent root, the database directory and the
// authoritative member descriptors present when it was opened. A retained
// member cannot be unlinked and its inode number reused while held. Recheck
// and Schema inspect only these handles: no store, host lock or control
// database access, so the store may call them inside its write transaction.
type sqliteSource struct {
	mu          sync.RWMutex
	closed      bool
	root        *os.File
	rootPath    string
	dir         *os.File
	components  []string
	filename    string
	database    data.DatabaseName
	databaseID  data.DatabaseID
	definitions []data.SQLiteSchemaDefinition
	location    sqliteprep.Location
	members     map[string]*os.File
	proofs      map[string]memberProof
	hold        *store.SQLiteSourceHold
}

// memberProof caches a content digest by the full change identity. A content
// write always advances ctime, which an unprivileged process cannot set.
type memberProof struct {
	identity [6]uint64
	digest   string
}

func openSQLiteSource(b data.SQLiteDatabaseBinding, definitions []data.SQLiteSchemaDefinition, hold *store.SQLiteSourceHold) (source *sqliteSource, err error) {
	relative, err := data.RelativeDirectory(b.IncarnationID, b.DatabaseID)
	if err != nil || relative != b.RelativeDirectory || !data.ValidRoot(string(b.Root)) || b.Filename == "" || strings.ContainsAny(string(b.Filename), "/\x00") {
		return nil, errSQLiteSource
	}
	s := &sqliteSource{rootPath: string(b.Root), components: strings.Split(relative, "/"), filename: string(b.Filename), database: b.Name, databaseID: b.DatabaseID, definitions: definitions, members: map[string]*os.File{}, proofs: map[string]memberProof{}, hold: hold}
	defer func() {
		if err != nil {
			err = errors.Join(err, s.closeHandles())
		}
	}()
	if s.root, err = openPathNoFollow(string(b.Root)); err != nil {
		return nil, errSQLiteSource
	}
	if s.dir, err = s.walk(); err != nil {
		return nil, err
	}
	if s.location, err = s.observeLocation(); err != nil {
		return nil, err
	}
	for _, suffix := range []string{"", "-wal", "-journal"} {
		if err = s.retain(s.filename + suffix); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// openPathNoFollow walks an absolute directory path without following links.
func openPathNoFollow(path string) (*os.File, error) {
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if part == "" {
			continue
		}
		next, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(fd)
		if err != nil {
			return nil, err
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), path), nil
}

// walk resolves the database directory beneath the retained root without
// following links. Recheck repeats it to prove the name still reaches the
// retained directory.
func (s *sqliteSource) walk() (*os.File, error) {
	fd, err := unix.FcntlInt(s.root.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, errSQLiteSource
	}
	for _, part := range s.components {
		next, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(fd)
		if err != nil {
			return nil, errSQLiteSource
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), s.filename), nil
}

func statxFD(fd int, name string) (unix.Statx_t, error) {
	var stat unix.Statx_t
	flags := unix.AT_SYMLINK_NOFOLLOW | unix.AT_STATX_SYNC_AS_STAT
	if name == "" {
		flags |= unix.AT_EMPTY_PATH
	}
	err := unix.Statx(fd, name, flags, unix.STATX_BASIC_STATS|unix.STATX_MNT_ID, &stat)
	if err == nil && stat.Mask&unix.STATX_MNT_ID == 0 {
		err = errSQLiteSource
	}
	return stat, err
}

// observeLocation requires a private runner-owned directory on the root's
// own mount: a bind mount over the directory changes its mount ID.
func (s *sqliteSource) observeLocation() (sqliteprep.Location, error) {
	root, err := statxFD(int(s.root.Fd()), "")
	if err != nil {
		return sqliteprep.Location{}, errSQLiteSource
	}
	dir, err := statxFD(int(s.dir.Fd()), "")
	if err != nil || dir.Mode&unix.S_IFMT != unix.S_IFDIR || dir.Mnt_id != root.Mnt_id || dir.Dev_major != root.Dev_major || dir.Dev_minor != root.Dev_minor || dir.Uid != uint32(os.Geteuid()) { //nolint:gosec // Runner IDs are nonnegative.
		return sqliteprep.Location{}, errSQLiteSource
	}
	l := sqliteprep.Location{Device: unix.Mkdev(dir.Dev_major, dir.Dev_minor), Inode: dir.Ino, MountID: dir.Mnt_id, UID: dir.Uid, GID: dir.Gid, Mode: uint32(dir.Mode & 0o7777)}
	return l, l.Validate()
}

func (s *sqliteSource) retain(name string) error {
	fd, err := unix.Openat(int(s.dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return errSQLiteSource
	}
	s.members[name] = os.NewFile(uintptr(fd), name)
	return nil
}

// Recheck returns the current stopped bundle. A replaced directory, a changed
// mount or a different authoritative inode yields a bundle that cannot match
// the recorded baseline; a closed source fails.
func (s *sqliteSource) Recheck(ctx context.Context) (sqliteprep.StoppedBundle, error) {
	s.mu.Lock() // Exclusive: the proof cache is updated.
	defer s.mu.Unlock()
	if s.closed {
		return sqliteprep.StoppedBundle{}, errSQLiteSource
	}
	if err := s.sameDirectory(); err != nil {
		return sqliteprep.StoppedBundle{}, err
	}
	location, err := s.observeLocation()
	if err != nil || location != s.location {
		return sqliteprep.StoppedBundle{}, errSQLiteSource
	}
	var members [4]sqliteprep.Member
	for i, suffix := range sqliteSourceSuffixes {
		if members[i], err = s.member(ctx, s.filename+suffix, suffix != "-shm"); err != nil {
			return sqliteprep.StoppedBundle{}, err
		}
	}
	bundle := sqliteprep.StoppedBundle{Location: location, DB: members[0], WAL: members[1], SHM: members[2], Journal: members[3]}
	return bundle, bundle.Validate()
}

func (s *sqliteSource) sameDirectory() error {
	if err := s.sameRoot(); err != nil {
		return err
	}
	current, err := s.walk()
	if err != nil {
		return err
	}
	defer func() { _ = current.Close() }()
	a, errA := statxFD(int(current.Fd()), "")
	b, errB := statxFD(int(s.dir.Fd()), "")
	if errA != nil || errB != nil || a.Ino != b.Ino || a.Dev_major != b.Dev_major || a.Dev_minor != b.Dev_minor || a.Mnt_id != b.Mnt_id {
		return errSQLiteSource
	}
	return nil
}

// sameRoot proves the configured absolute name still reaches the held root.
// Rewalking every ancestor without following links also rejects a replaced
// ancestor which leaves the retained tree accessible at a detached old name.
func (s *sqliteSource) sameRoot() error {
	current, err := openPathNoFollow(s.rootPath)
	if err != nil {
		return errSQLiteSource
	}
	defer func() { _ = current.Close() }()
	named, namedErr := statxFD(int(current.Fd()), "")
	held, heldErr := statxFD(int(s.root.Fd()), "")
	if namedErr != nil || heldErr != nil || named.Ino != held.Ino || named.Dev_major != held.Dev_major || named.Dev_minor != held.Dev_minor || named.Mnt_id != held.Mnt_id {
		return errSQLiteSource
	}
	return nil
}

func (s *sqliteSource) member(ctx context.Context, name string, authoritative bool) (sqliteprep.Member, error) {
	stat, err := statxFD(int(s.dir.Fd()), name)
	if errors.Is(err, unix.ENOENT) {
		return sqliteprep.Member{Presence: sqliteprep.Absent}, nil
	}
	if err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return sqliteprep.Member{}, errSQLiteSource
	}
	m := sqliteprep.Member{Presence: sqliteprep.Regular, Device: unix.Mkdev(stat.Dev_major, stat.Dev_minor), Inode: stat.Ino, MountID: stat.Mnt_id, UID: stat.Uid, GID: stat.Gid, Mode: uint32(stat.Mode & 0o7777), Links: uint64(stat.Nlink), Size: int64(stat.Size), MtimeNS: stat.Mtime.Sec*1e9 + int64(stat.Mtime.Nsec)} //nolint:gosec // Bounded kernel stat fields.
	if !authoritative {
		return m, nil
	}
	m.ProofSHA256, err = s.proof(ctx, name, stat)
	return m, err
}

// proof digests the member's content through its retained descriptor when the
// name still reaches it, otherwise through a fresh descriptor whose identity
// equals the stat. Unchanged identity reuses the earlier digest.
func (s *sqliteSource) proof(ctx context.Context, name string, stat unix.Statx_t) (string, error) {
	identity := [6]uint64{stat.Ino, stat.Size, uint64(stat.Mtime.Sec), uint64(stat.Mtime.Nsec), uint64(stat.Ctime.Sec), uint64(stat.Ctime.Nsec)} //nolint:gosec // Stat timestamps are nonnegative.
	if cached, ok := s.proofs[name]; ok && cached.identity == identity {
		return cached.digest, nil
	}
	file := s.members[name]
	if file == nil || !sameInode(file, stat) {
		fd, err := unix.Openat(int(s.dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
		if err != nil {
			return "", errSQLiteSource
		}
		file = os.NewFile(uintptr(fd), name)
		defer func() { _ = file.Close() }()
		if !sameInode(file, stat) {
			return "", errSQLiteSource
		}
	}
	digest, err := digestFile(ctx, file, int64(stat.Size)) //nolint:gosec // Regular file sizes fit int64.
	if err != nil {
		return "", err
	}
	if _, retained := s.members[name]; retained {
		s.proofs[name] = memberProof{identity: identity, digest: digest}
	}
	return digest, nil
}

func sameInode(file *os.File, stat unix.Statx_t) bool {
	held, err := statxFD(int(file.Fd()), "")
	return err == nil && held.Ino == stat.Ino && held.Dev_major == stat.Dev_major && held.Dev_minor == stat.Dev_minor
}

// digestFile reads exactly size bytes from offset zero, honoring cancellation.
func digestFile(ctx context.Context, file *os.File, size int64) (string, error) {
	hash := sha256.New()
	buffer := make([]byte, 1<<20)
	for offset := int64(0); offset < size; {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, err := file.ReadAt(buffer[:min(int64(len(buffer)), size-offset)], offset)
		hash.Write(buffer[:n])
		offset += int64(n)
		if err != nil && (!errors.Is(err, io.EOF) || offset != size) {
			return "", errSQLiteSource
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// Schema observes the retained database through its retained directory
// descriptor, read-only and query-only. SQLite may rebuild its SHM index.
func (s *sqliteSource) Schema(ctx context.Context) (data.SQLiteSchemaObservation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return data.SQLiteSchemaObservation{}, errSQLiteSource
	}
	path := "/proc/self/fd/" + strconv.Itoa(int(s.dir.Fd())) + "/" + s.filename
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Set("mode", "ro")
	q.Add("_pragma", "query_only(ON)")
	q.Add("_pragma", "busy_timeout(5000)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return data.SQLiteSchemaObservation{}, errSQLiteSource
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return data.SQLiteSchemaObservation{}, errSQLiteSource
	}
	defer func() { _ = tx.Rollback() }()
	observed := data.ObserveSQLiteSchemaTransaction(ctx, tx, s.database, s.definitions)
	observed.DatabaseID = s.databaseID
	if observed.State == data.Unknown {
		return observed, errSQLiteSource
	}
	return observed, tx.Commit()
}

func (s *sqliteSource) SQLiteSourceHold() *store.SQLiteSourceHold { return s.hold }

// Close waits for any in-flight Recheck or Schema, so it cannot invalidate
// the handles of a completion transaction that is still checking them.
func (s *sqliteSource) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	err := s.closeHandles()
	if s.hold != nil {
		err = errors.Join(err, s.hold.Release())
	}
	return err
}

func (s *sqliteSource) closeHandles() error {
	var errs []error
	for name, file := range s.members {
		errs = append(errs, file.Close())
		delete(s.members, name)
	}
	for _, file := range []*os.File{s.dir, s.root} {
		if file != nil {
			errs = append(errs, file.Close())
		}
	}
	s.dir, s.root = nil, nil
	return errors.Join(errs...)
}

// observeSQLiteLocation is the journal's pre-stop location observer. It
// retains nothing; the store admits it against protected allocation evidence.
func observeSQLiteLocation(_ context.Context, b data.SQLiteDatabaseBinding) (sqliteprep.Location, error) {
	source, err := openSQLiteSource(b, nil, nil)
	if err != nil {
		return sqliteprep.Location{}, err
	}
	location := source.location
	return location, source.Close()
}

// observeSQLiteSchema is the journal's pre-stop schema observer with the
// registered definitions. Writers may still run; every later held observation
// must name the identical schema.
func observeSQLiteSchema(ctx context.Context, b data.SQLiteDatabaseBinding, definitions []data.SQLiteSchemaDefinition) data.SQLiteSchemaObservation {
	observed := data.ObserveSQLiteSchema(ctx, b, definitions)
	observed.DatabaseID = b.DatabaseID
	return observed
}
