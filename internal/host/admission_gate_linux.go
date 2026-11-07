//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/ShaulLavo/brine/internal/admission"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/target"
)

const admissionRoot = "/var/lib/brine-admission"

var udpAppName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

func acquireStateLease(ctx context.Context, exempt *admission.Exemption) (*admission.Lease, error) {
	directory, err := admission.OpenDirectory(admissionRoot)
	if err != nil {
		return nil, err
	}
	var lease *admission.Lease
	if exempt == nil {
		lease, err = directory.Acquire(ctx)
	} else {
		lease, err = directory.AcquireExempt(ctx, *exempt)
	}
	closeErr := directory.Close()
	if err != nil || closeErr != nil {
		if lease != nil {
			closeErr = errors.Join(closeErr, lease.Close())
		}
		return nil, errors.Join(err, closeErr)
	}
	return lease, nil
}

func openState(ctx context.Context, dir string, mode stateMode, exempt *admission.Exemption) (*gatedStore, error) {
	lease, err := acquireStateLease(ctx, exempt)
	if err != nil {
		return nil, err
	}
	return openLeasedState(ctx, dir, mode, lease)
}

func openLeasedState(ctx context.Context, dir string, mode stateMode, lease *admission.Lease) (*gatedStore, error) {
	var state *store.Store
	var err error
	switch mode {
	case stateMutating:
		state, err = store.OpenContext(ctx, dir)
	case stateReadOnly:
		state, err = store.OpenReadOnly(ctx, dir)
	case stateSnapshot:
		state, err = store.OpenSnapshotReadOnly(ctx, dir)
	case statePreview:
		state, err = store.OpenPreviewReadOnly(ctx, dir)
	default:
		err = admission.ErrUnavailable
	}
	if err != nil {
		return nil, errors.Join(err, lease.Close())
	}
	return &gatedStore{Store: state, lease: lease}, nil
}

func readControlInventory(ctx context.Context, dir string) (_ target.ControlInventory, err error) {
	lease, err := acquireStateLease(ctx, nil)
	if err != nil {
		return target.ControlInventory{}, err
	}
	defer func() { err = errors.Join(err, lease.Close()) }()
	empty, err := store.EmptyStateDirectory(ctx, dir)
	if err != nil {
		return target.ControlInventory{}, err
	}
	if empty {
		return target.ControlInventory{Apps: []target.ControlApp{}}, nil
	}
	state, err := openLeasedState(ctx, dir, stateReadOnly, lease)
	if err != nil {
		return target.ControlInventory{}, err
	}
	defer func() { err = errors.Join(err, state.Close()) }()
	return state.InventoryState(ctx)
}

// ControlMigrate is the fixed root-issued update child. Public selectors do not
// grant admission; the protected state and held stage lease must match them.
func ControlMigrate(ctx context.Context, e admission.Exemption) (err error) {
	if e.Kind != admission.Update || e.Stage != admission.Migrating {
		return admission.ErrClosed
	}
	identity, err := user.LookupId(strconv.Itoa(os.Geteuid()))
	if err != nil || os.Geteuid() == 0 || identity.Username != "brine" || identity.HomeDir != "/home/brine" {
		return admission.ErrUnavailable
	}
	state, err := openState(ctx, filepath.Join(identity.HomeDir, ".local/state/brine"), stateMutating, &e)
	if err != nil {
		return err
	}
	return state.Close()
}

// UDPGrantHeld is the unprivileged child called under the operator's UDP fence.
func UDPGrantHeld(ctx context.Context, app string) (_ bool, err error) {
	identity, err := user.LookupId(strconv.Itoa(os.Geteuid()))
	if err != nil || os.Geteuid() == 0 || identity.Username != "brine" || identity.HomeDir != "/home/brine" || !udpAppName.MatchString(app) {
		return false, admission.ErrUnavailable
	}
	return readUDPGrantHeld(ctx, filepath.Join(identity.HomeDir, ".local/state/brine"), app)
}

func readUDPGrantHeld(ctx context.Context, dir, app string) (_ bool, err error) {
	lease, err := acquireStateLease(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { err = errors.Join(err, lease.Close()) }()
	// This store reader includes the exact empty-or-mutation-lock initial case.
	return store.ReadUDPGrantHeld(ctx, dir, app)
}
