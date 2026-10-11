package data

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

const MaxSQLiteCaptureBytes = 64 << 20
const MaxCaptureBudget = 3 * time.Minute

// CaptureTarget selects bounded engine capture only. The operation owner must
// establish and retain its durable fence and independent writer quiescence.
type CaptureTarget struct {
	kind   EngineKind
	sqlite SQLiteCaptureRequest
}
type SQLiteCaptureRequest struct {
	Binding     SQLiteDatabaseBinding
	StagingRoot string
	PointID     string
	MaxBytes    int64
}
type CaptureResult struct {
	kind   EngineKind
	sqlite []byte
}

func SQLiteCaptureTarget(request SQLiteCaptureRequest) CaptureTarget {
	return CaptureTarget{kind: EngineSQLite, sqlite: request}
}
func SpacetimeDBCaptureTarget() CaptureTarget  { return CaptureTarget{kind: EngineSpacetimeDB} }
func (r CaptureResult) SQLite() ([]byte, bool) { return r.sqlite, r.kind == EngineSQLite }
func CaptureEngine(ctx context.Context, target CaptureTarget, budget time.Duration) (CaptureResult, error) {
	if ctx.Err() != nil || budget <= 0 || budget > MaxCaptureBudget {
		return CaptureResult{}, ErrInvalid
	}
	switch target.kind {
	case EngineSQLite:
		ctx, cancel := context.WithTimeout(ctx, budget)
		defer cancel()
		raw, err := captureSQLiteSnapshot(ctx, target.sqlite)
		if err != nil {
			return CaptureResult{}, err
		}
		return CaptureResult{kind: EngineSQLite, sqlite: raw}, nil
	case EngineSpacetimeDB:
		return CaptureResult{}, ErrUnsupportedEngine
	default:
		return CaptureResult{}, ErrInvalid
	}
}
func captureSQLiteSnapshot(ctx context.Context, r SQLiteCaptureRequest) (raw []byte, err error) {
	b := r.Binding
	relative, err := RelativeDirectory(b.IncarnationID, b.DatabaseID)
	if err != nil || relative != b.RelativeDirectory || !ValidID(r.PointID) || !ValidRoot(r.StagingRoot) || r.MaxBytes <= 0 || r.MaxBytes > MaxSQLiteCaptureBytes {
		return nil, ErrInvalid
	}
	declaration := SQLiteDatabase{Name: b.Name, PersistentRoot: b.Root, MountPath: b.MountPath, Filename: b.Filename, BackupDestination: "capture"}
	if declaration.Validate() != nil || OverlappingPaths(r.StagingRoot, string(b.Root)) || verifyPrivateTree(r.StagingRoot, r.StagingRoot) != nil || verifyPrivateTree(string(b.Root), filepath.Join(string(b.Root), relative)) != nil {
		return nil, ErrInvalid
	}
	source := filepath.Join(string(b.Root), relative, string(b.Filename))
	sourceInfo, err := captureSQLiteSource(source, r.MaxBytes)
	if err != nil {
		return nil, err
	}
	handle, err := os.OpenRoot(r.StagingRoot)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, handle.Close()) }()
	if err = handle.Mkdir(r.PointID, 0700); err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, handle.RemoveAll(r.PointID)) }()
	output := filepath.Join(r.StagingRoot, r.PointID, "snapshot.sqlite")
	uri := url.URL{Scheme: "file", Path: source}
	query := uri.Query()
	query.Set("mode", "ro")
	uri.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, err
	}
	_, copyErr := db.ExecContext(ctx, "VACUUM INTO ?", output)
	err = errors.Join(copyErr, db.Close())
	if err != nil {
		return nil, err
	}
	if err = os.Chmod(output, 0600); err != nil {
		return nil, err
	}
	info, err := os.Lstat(output)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > r.MaxBytes {
		return nil, ErrInvalid
	}
	if err = VerifyRunnerFile(output); err != nil {
		return nil, err
	}
	after, err := os.Lstat(source)
	if err != nil || !os.SameFile(sourceInfo, after) {
		return nil, ErrInvalid
	}
	raw, err = handle.ReadFile(filepath.Join(r.PointID, "snapshot.sqlite"))
	return raw, err
}
func captureSQLiteSource(source string, maxBytes int64) (os.FileInfo, error) {
	if err := VerifyRunnerFile(source); err != nil {
		return nil, err
	}
	sourceInfo, err := os.Lstat(source)
	if err != nil || sourceInfo.Size() <= 0 || sourceInfo.Size() > maxBytes {
		return nil, ErrInvalid
	}
	total := sourceInfo.Size()
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		info, statErr := os.Lstat(source + suffix)
		if os.IsNotExist(statErr) {
			continue
		}
		if statErr != nil || VerifyRunnerFile(source+suffix) != nil || info.Size() < 0 || info.Size() > maxBytes {
			return nil, ErrInvalid
		}
		total += info.Size()
		if total > maxBytes {
			return nil, ErrInvalid
		}
	}
	return sourceInfo, nil
}
