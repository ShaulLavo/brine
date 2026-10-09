package host

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/reconcile"
	"github.com/ShaulLavo/brine/internal/store"
)

type previewFile struct {
	Bytes    string
	Mode     os.FileMode
	Modified time.Time
}

func previewFiles(t *testing.T, dir string) map[string]previewFile {
	t.Helper()
	files := map[string]previewFile{}
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		var data []byte
		if !info.IsDir() {
			data, err = os.ReadFile(path)
			if err != nil {
				return err
			}
		}
		files[path] = previewFile{string(data), info.Mode(), info.ModTime()}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}
func TestPreviewNeverInitializesOrMigratesDatabase(t *testing.T) {
	for _, fixture := range []string{"missing", "v1", "old"} {
		t.Run(fixture, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Chmod(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if fixture != "missing" {
				s, err := store.Open(dir)
				if err != nil {
					t.Fatal(err)
				}
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				if fixture == "old" || fixture == "v1" {
					db, err := sql.Open("sqlite", filepath.Join(dir, "control.db"))
					if err != nil {
						t.Fatal(err)
					}
					if _, err = db.Exec("UPDATE schema_version SET version=?", map[string]int{"old": 0, "v1": 1}[fixture]); err != nil {
						t.Fatal(err)
					}
					db.Close()
				}
			}
			before := previewFiles(t, dir)
			writable := 0
			f := newServerFactory("fixture", "deploy", func(context.Context, string) (*Runtime, error) {
				writable++
				s, err := store.Open(dir)
				if err != nil {
					return nil, err
				}
				return &Runtime{close: s.Close}, nil
			}, nil)
			// Inject only the protected state path; exercise the real preview initializer.
			f.previewOpen = func(ctx context.Context, _ string) (*Runtime, error) {
				r, e := openPreviewState(ctx, dir)
				if e != nil {
					return nil, e
				}
				if r.previewStore != nil {
					r.Reconciler = controlPreview{"preview_unavailable"}
				}
				return r, nil
			}
			server := dispatch.NewServer("fixture", nil)
			server.Factory = f.Build
			data, _ := json.Marshal(dispatch.Request{SchemaVersion: dispatch.SchemaVersion, RequestID: "preview", Op: "reconcile", Args: json.RawMessage(`{"dry_run":true}`)})
			response, err := server.Handle(context.Background(), bytes.NewReader(data))
			if err != nil || !response.OK {
				t.Fatalf("preview: %#v %v", response, err)
			}
			report := response.Data.(reconcile.Report)
			want := "preview_unavailable"
			if fixture == "missing" {
				want = "database_missing"
			}
			if fixture == "old" || fixture == "v1" {
				want = "schema_upgrade_required"
			}
			if report.ControlState != want {
				t.Fatalf("report %#v want %s", report, want)
			}
			encoded, _ := json.Marshal(report)
			if _, err := reconcile.DecodeReport(encoded); err != nil {
				t.Fatal(err)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			if writable != 0 {
				t.Fatal("preview opened writable runtime")
			}
			if after := previewFiles(t, dir); !reflect.DeepEqual(before, after) {
				t.Fatalf("preview changed files: before=%#v after=%#v", before, after)
			}
		})
	}
}

func TestPreviewLiveWALDoesNotChangeFiles(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// Leave a live WAL and provision the existing fences just as normal launches do.
	launch, err := s.AcquireLaunchLock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	launch.Release()
	host, err := s.AcquireHostLock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	host.Release()
	before := previewFiles(t, dir)
	r, err := openPreviewState(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.previewStore.ListUnfinished(context.Background()); err != nil {
		t.Fatal(err)
	}
	lock, err := r.previewStore.AcquireLaunchLock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	lock.Release()
	lock, err = r.previewStore.AcquireHostLock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	lock.Release()
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if after := previewFiles(t, dir); !reflect.DeepEqual(before, after) {
		t.Fatal("live-WAL preview changed state files")
	}
}
