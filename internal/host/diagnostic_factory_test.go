package host

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/diagnose"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/inventory"
	"github.com/ShaulLavo/brine/internal/store"
)

func TestFactoryDiagnosisNeverOpensMutationRuntime(t *testing.T) {
	opens := 0
	f := newServerFactory("fixture", "deploy", func(context.Context, string) (*Runtime, error) {
		opens++
		return &Runtime{close: func() error { return nil }}, nil
	}, func(context.Context) (dispatch.Inventory, error) { return nil, nil })
	server, err := f.Build(context.Background(), "diagnose")
	if err != nil {
		t.Fatal(err)
	}
	if opens != 0 {
		t.Fatalf("diagnose opened mutation runtime %d times", opens)
	}
	if server.Diagnose == nil {
		t.Fatal("missing diagnostic reader")
	}
	if server.Planner != nil || server.Reconciler != nil || server.Apps != nil || server.Config != nil || server.Secrets != nil {
		t.Fatal("diagnostic server has mutation capabilities")
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestProductionFactoryDiagnosticState(t *testing.T) {
	for _, kind := range []string{"missing", "old", "current", "permissions"} {
		t.Run(kind, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "state")
			if kind != "missing" {
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
				state, err := store.Open(dir)
				if err != nil {
					t.Fatal(err)
				}
				if err := state.Close(); err != nil {
					t.Fatal(err)
				}
				{
					db, err := sql.Open("sqlite", filepath.Join(dir, "control.db"))
					if err != nil {
						t.Fatal(err)
					}
					if _, err := db.Exec("PRAGMA journal_mode=DELETE"); err != nil {
						t.Fatal(err)
					}
					if kind == "old" {
						if _, err := db.Exec("UPDATE schema_version SET version = 0"); err != nil {
							t.Fatal(err)
						}
					}
					if err := db.Close(); err != nil {
						t.Fatal(err)
					}
				}
				if kind == "permissions" {
					if err := os.Chmod(dir, 0750); err != nil {
						t.Fatal(err)
					}
				}
			}
			before := diagnosticFiles(t, dir)
			f := NewServerFactory("fixture", "deploy")
			f.diagnosticStateDir = dir
			f.inventory = func(context.Context) (dispatch.Inventory, error) { return &inventory.Collector{}, nil }
			server, err := f.Build(context.Background(), "diagnose")
			if err != nil {
				t.Fatal(err)
			}
			reader := server.Diagnose.(diagnose.Reader)
			_, err = reader.Store.AppNames(context.Background())
			if (kind == "current") != (err == nil) {
				t.Fatalf("kind=%s read error=%v", kind, err)
			}
			readCollector := reader.Inventory.(*inventory.Collector)
			_, inventoryErr := readCollector.StateInventory(context.Background())
			if (kind == "current") != (inventoryErr == nil) {
				t.Fatalf("kind=%s inventory read error=%v", kind, inventoryErr)
			}
			reader.Inventory, reader.Runner, reader.FS, reader.Logs, reader.MinimumFreeDiskBytes = nil, nil, nil, nil, nil
			report, reportErr := reader.Read(context.Background(), diagnose.Request{})
			if reportErr != nil || report.AppNames.Value != nil {
				t.Fatalf("missing inventory must remain a partial report: %+v %v", report.AppNames, reportErr)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			after := diagnosticFiles(t, dir)
			if len(before) != len(after) {
				t.Fatal("diagnosis changed durable file count")
			}
			for path, content := range before {
				if !bytes.Equal(content, after[path]) {
					t.Fatalf("diagnosis changed %s", path)
				}
			}
		})
	}
}

func diagnosticFiles(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		files[path+"/mode"] = []byte(info.Mode().String())
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			files[path] = data
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestProductionFactoryDiagnosisDoesNotTakeMutationLock(t *testing.T) {
	dir := t.TempDir()
	state, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	lock, err := state.AcquireHostLock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	f := NewServerFactory("fixture", "deploy")
	f.diagnosticStateDir = dir
	f.inventory = func(context.Context) (dispatch.Inventory, error) { return nil, nil }
	server, err := f.Build(context.Background(), "diagnose")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := server.Diagnose.(diagnose.Reader).Store.AppNames(ctx); err != nil {
		t.Fatalf("diagnosis blocked by mutation lock: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}
