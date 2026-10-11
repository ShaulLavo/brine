package restic

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type recordingAdmission struct{ calls int }

func (a *recordingAdmission) AdmitRepository(context.Context, Scope, RepositoryAction, string) error {
	a.calls++
	return ErrInvalid
}

func TestClientRechecksAllBundleQuotasBeforeRepositoryAccess(t *testing.T) {
	source, work := testTree(t)
	if err := os.WriteFile(filepath.Join(source, "second"), []byte("confirmed fixture state"), 0600); err != nil {
		t.Fatal(err)
	}
	bundle, err := Seal(context.Background(), source, work, testIdentity(), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer bundle.Close()
	point := TransportPoint{Identity: testIdentity(), Scope: testScope(), SnapshotID: strings.Repeat("a", 64), ManifestSHA256: bundle.manifest.SHA256(), ArchiveSHA256: bundle.sha256, ArchiveBytes: bundle.size}
	for _, quota := range []string{"file", "total", "archive", "members"} {
		t.Run(quota, func(t *testing.T) { checkClientQuota(t, quota, bundle, point, work) })
	}
}

func checkClientQuota(t *testing.T, quota string, bundle Bundle, point TransportPoint, work string) {
	t.Helper()
	limits := DefaultLimits()
	switch quota {
	case "file":
		limits.FileBytes = 1
	case "total":
		limits.FileBytes = bundle.manifest.document.Members[0].Size
		limits.Bytes = 2*limits.FileBytes - 1
	case "archive":
		limits.Bytes = 2 * bundle.manifest.document.Members[0].Size
		limits.FileBytes, limits.ArchiveBytes = limits.Bytes, limits.Bytes
	case "members":
		limits.Members = 1
	}
	admission := &recordingAdmission{}
	client, err := NewClient(strings.Repeat("a", 64), limits, admission)
	if err != nil {
		t.Fatal(err)
	}
	repository := Repository{scope: testScope(), private: func() []string { t.Fatal("quota refusal accessed private material"); return nil }}
	_, backupErr := client.Backup(context.Background(), repository, work, bundle)
	_, inspectErr := client.InspectCapture(context.Background(), repository, work, bundle)
	restoreErr := client.RestoreExact(context.Background(), repository, work, filepath.Join(work, "refused"), point, bundle.Manifest())
	for _, err := range []error{backupErr, inspectErr, restoreErr} {
		if !errors.Is(err, ErrLimit) {
			t.Fatalf("%s quota not refused: %v", quota, err)
		}
	}
	if admission.calls != 0 {
		t.Fatal("quota refusal reached repository admission")
	}
}
