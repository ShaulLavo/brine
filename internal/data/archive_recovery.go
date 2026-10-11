package data

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"
)

// ObserveArchivedSchema admits only the ID-derived archived allocation, never a
// caller-supplied alternate path or a recreated app's live directory.
func ObserveArchivedSchema(ctx context.Context, b SQLiteDatabaseBinding, a AllocationReceipt, id ArchiveID, definitions []SQLiteSchemaDefinition) SQLiteSchemaObservation {
	if VerifyArchivedAllocation(b, a, id) != nil {
		return SQLiteSchemaObservation{State: Unknown, DatabaseID: b.DatabaseID, ObservedAt: time.Now().UTC(), UnknownReason: "unverified_path"}
	}
	return observeSchemaDirectory(ctx, b, definitions, filepath.Join("archives", string(id), "databases", string(b.DatabaseID)))
}
func VerifyArchivedEmpty(b SQLiteDatabaseBinding, a AllocationReceipt, id ArchiveID) (err error) {
	if err = VerifyArchivedAllocation(b, a, id); err != nil {
		return err
	}
	root, err := os.OpenRoot(string(b.Root))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	directory, err := root.Open(filepath.Join("archives", string(id), "databases", string(b.DatabaseID)))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, directory.Close()) }()
	entries, readErr := directory.ReadDir(1)
	if len(entries) != 0 || !errors.Is(readErr, io.EOF) {
		return ErrInvalid
	}
	return VerifyArchivedAllocation(b, a, id)
}
