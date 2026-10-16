package archive

import (
	"context"
	"errors"
	"testing"
)

type retiredBackupRig struct {
	*recoveryRig
	backupCleanups int
	backupError    error
}

func (r *retiredBackupRig) RetireArchivedBackups(context.Context, Plan) error {
	if r.state.Stage != Retired {
		return ErrRefused
	}
	r.backupCleanups++
	return r.backupError
}

func TestRetiredArchiveReplayCleansOnlyItsBackupSchedule(t *testing.T) {
	for _, reconcile := range []bool{false, true} {
		s, base := recoveryFixture(t)
		out, err := s.Retire(t.Context(), base.p.ArchiveID)
		if err != nil || out.Stage != Retired {
			t.Fatal(out, err)
		}
		r := &retiredBackupRig{recoveryRig: base}
		s.Host = r
		r.writersRunning = true
		r.failCheck = true
		r.failPreflight = true
		replay := s.Retire
		if reconcile {
			replay = s.Reconcile
		}
		again, err := replay(t.Context(), r.p.ArchiveID)
		if err != nil || again != out || r.backupCleanups != 1 || r.cleanup != 1 {
			t.Fatal("retired replay left its backup schedule installed", reconcile, again, err, r.backupCleanups)
		}
		r.backupError = errors.New("interrupted scheduler removal")
		if _, err = replay(t.Context(), r.p.ArchiveID); !errors.Is(err, ErrRecovery) {
			t.Fatal("unknown backup cleanup reported success", err)
		}
		r.backupError = nil
		if again, err = replay(t.Context(), r.p.ArchiveID); err != nil || again != out || r.cleanup != 1 {
			t.Fatal("backup cleanup changed the terminal archive", again, err)
		}
	}
}
