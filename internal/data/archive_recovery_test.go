//go:build linux

package data

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchivedSchemaAndEmptyEvidenceUseExactAllocation(t *testing.T) {
	tree, inc, id := archiveMoveFixture(t)
	database := DatabaseID(strings.Repeat("c", 32))
	relative, err := RelativeDirectory(inc, database)
	if err != nil {
		t.Fatal(err)
	}
	binding := DatabaseBinding{DatabaseID: database, IncarnationID: inc, Root: tree.Root, RelativeDirectory: relative, Name: "main", MountPath: "/data", Filename: "app.sqlite"}
	if err = os.MkdirAll(filepath.Join(string(tree.Root), relative), 0700); err != nil {
		t.Fatal(err)
	}
	allocation, err := CaptureAllocation(binding)
	if err != nil {
		t.Fatal(err)
	}
	if err = RenameArchiveTree(tree, inc, id); err != nil {
		t.Fatal(err)
	}
	if err = VerifyArchivedEmpty(binding, allocation, id); err != nil {
		t.Fatal("empty archive refused", err)
	}
	filename := filepath.Join(string(tree.Root), "archives", string(id), "databases", string(database), "app.sqlite")
	db, err := sql.Open("sqlite", filename)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := db.Exec("VACUUM")
	closeErr := db.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatal(writeErr, closeErr)
	}
	if err = os.Chmod(filename, 0600); err != nil {
		t.Fatal(err)
	}
	if err = VerifyArchivedEmpty(binding, allocation, id); err == nil {
		t.Fatal("written archive called never-written")
	}
	observed := ObserveArchivedSchema(context.Background(), binding, allocation, id, nil)
	if observed.State != VerifiedEmpty || observed.DatabaseID != database {
		t.Fatal(observed)
	}
	wrong := allocation
	wrong.DirectoryInode++
	if observed = ObserveArchivedSchema(context.Background(), binding, wrong, id, nil); observed.State != Unknown {
		t.Fatal("replaced allocation admitted", observed)
	}
	if observed = ObserveArchivedSchema(context.Background(), binding, allocation, ArchiveID(strings.Repeat("d", 32)), nil); observed.State != Unknown {
		t.Fatal("wrong archive admitted", observed)
	}
	if observed = ObserveSchema(context.Background(), binding, nil); observed.State != Unknown {
		t.Fatal("live observer silently changed path", observed)
	}
}
