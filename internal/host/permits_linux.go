//go:build linux

package host

import (
	"context"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/backupcredentials"
	"github.com/ShaulLavo/brine/internal/quadlet"
	"github.com/ShaulLavo/brine/internal/replicapermits"
	"github.com/ShaulLavo/brine/internal/replication"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/target"
)

type PermitRuntime struct {
	Reader replication.LaunchReader
	state  *store.Store
}

func (r *PermitRuntime) Close() error { return r.state.Close() }

// OpenPermits opens read-only existing state. No policy migrations, credential
// writes, deployment managers, or mutation locks belong in the startup gate.
func OpenPermits(ctx context.Context) (*PermitRuntime, error) {
	if ctx.Err() != nil || os.Geteuid() == 0 {
		return nil, replication.ErrPermit
	}
	identity, err := user.LookupId(strconv.Itoa(os.Geteuid()))
	if err != nil || identity.Username != "brine" || identity.HomeDir != "/home/brine" {
		return nil, replication.ErrPermit
	}
	state, err := store.OpenReadOnly(ctx, filepath.Join(identity.HomeDir, ".local/state/brine"))
	if err != nil {
		return nil, replication.ErrPermit
	}
	evidence := writerEvidence{Operations: attemptOperation{uid: uint32(os.Geteuid()), home: identity.HomeDir}, Home: identity.HomeDir} //nolint:gosec // Linux UID/GID originate as unsigned 32-bit syscall identities.
	reader := launchPermits{StorePermits: replicapermits.StorePermits{State: state, Configs: replication.DiskConfigs{}, Writers: evidence}, State: state, Files: backupcredentials.Files{Root: filepath.Join(identity.HomeDir, ".local/state/brine/credentials")}}
	return &PermitRuntime{Reader: reader, state: state}, nil
}

type writerEvidence struct {
	Operations replicapermits.OperationEvidence
	Home       string
}

func (w writerEvidence) OperationActive(ctx context.Context, id string) (bool, error) {
	if w.Operations == nil {
		return false, replication.ErrPermit
	}
	return w.Operations.OperationActive(ctx, id)
}
func (w writerEvidence) CommittedUnitsMatch(ctx context.Context, units []target.Unit) (bool, error) {
	if len(units) == 0 || len(units) > 16 {
		return false, replication.ErrPermit
	}
	seen := map[string]bool{}
	for _, unit := range units {
		if seen[unit.Name] || unit.Hash == "" {
			return false, replication.ErrPermit
		}
		seen[unit.Name] = true
		if err := quadlet.VerifyCurrent(ctx, w.Home, unit.Name, unit.Hash); err != nil {
			return false, err
		}
	}
	return ctx.Err() == nil, ctx.Err()
}

type launchPermits struct {
	replicapermits.StorePermits
	State *store.Store
	Files backupcredentials.Files
}

func (r launchPermits) ReadCredentialEnvironment(ctx context.Context, path string) ([]string, error) {
	if ctx.Err() != nil {
		return nil, replication.ErrPermit
	}
	credentials, err := r.Files.ReadPath(path)
	if err != nil {
		return nil, replication.ErrPermit
	}
	defer credentials.Clear()
	relative, err := filepath.Rel(r.Files.Root, path)
	if err != nil {
		return nil, replication.ErrPermit
	}
	parts := strings.Split(relative, "/")
	if len(parts) != 3 {
		return nil, replication.ErrPermit
	}
	version, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimPrefix(parts[2], "v"), ".env"), 10, 64)
	if err != nil {
		return nil, replication.ErrPermit
	}
	record, err := r.State.CredentialReceiptForReference(ctx, parts[1], version)
	if err != nil || record.ExpiresAt != nil && !time.Now().UTC().Before(*record.ExpiresAt) {
		return nil, replication.ErrPermit
	}
	if ctx.Err() != nil {
		return nil, replication.ErrPermit
	}
	return credentials.Environment(), nil
}
