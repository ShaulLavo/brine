//go:build linux

package host

import (
	"context"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"time"

	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/replicapermits"
	"github.com/ShaulLavo/brine/internal/replication"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/systemd"
)

// OpenWriterAttempt opens only the control store and a lazy read-only observer.
// The CLI validates the incarnation before opening this narrowly writable bundle.
func OpenWriterAttempt(ctx context.Context) (*WriterAttemptRuntime, error) {
	if ctx.Err() != nil || os.Geteuid() == 0 {
		return nil, replication.ErrPermit
	}
	identity, err := user.LookupId(strconv.Itoa(os.Geteuid()))
	if err != nil || identity.Username != "brine" || identity.HomeDir != "/home/brine" {
		return nil, replication.ErrPermit
	}
	state, err := store.OpenContext(ctx, filepath.Join(identity.HomeDir, ".local/state/brine"))
	if err != nil {
		return nil, replication.ErrPermit
	}
	return &WriterAttemptRuntime{state: state, attempt: replicapermits.WriterAttempts{State: state, Operations: attemptOperation{uid: uint32(os.Geteuid()), home: identity.HomeDir}}}, nil
}

// A committed restart never needs a subprocess or consumes allocation evidence.
type attemptOperation struct {
	uid  uint32
	home string
}

func (o attemptOperation) OperationActive(ctx context.Context, id string) (bool, error) {
	session, err := localexec.NewSession(localexec.ExecRunner{}, o.uid, o.home, 5*time.Second)
	if err != nil {
		return false, replication.ErrPermit
	}
	return (replicapermits.Operations{Units: systemd.New(session)}).OperationActive(ctx, id)
}
