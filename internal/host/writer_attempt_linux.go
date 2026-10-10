//go:build linux

package host

import (
	"context"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/replicapermits"
	"github.com/ShaulLavo/brine/internal/replication"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/systemd"
)

// WriterAttempt opens only the control store and a read-only operation-unit
// observer. It never builds deployment managers or reads agent credentials.
func WriterAttempt(ctx context.Context, incarnation string) (err error) {
	if ctx.Err() != nil || !data.ValidID(incarnation) || os.Geteuid() == 0 {
		return replication.ErrPermit
	}
	identity, err := user.LookupId(strconv.Itoa(os.Geteuid()))
	if err != nil || identity.Username != "brine" || identity.HomeDir != "/home/brine" {
		return replication.ErrPermit
	}
	state, err := store.OpenContext(ctx, filepath.Join(identity.HomeDir, ".local/state/brine"))
	if err != nil {
		return replication.ErrPermit
	}
	defer func() {
		if state.Close() != nil {
			err = replication.ErrPermit
		}
	}()
	return (replicapermits.WriterAttempts{State: state, Operations: attemptOperation{uid: uint32(os.Geteuid()), home: identity.HomeDir}}).WriterAttempt(ctx, incarnation)
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
