//go:build linux

package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/restore"
	"github.com/ShaulLavo/brine/internal/sqliteprep"
)

func (g *sqliteHostGuard) bindUnreleased(ctx context.Context) error {
	metadata, err := g.host.state.ReadSQLiteUnreleasedMetadata(ctx, g.input)
	if err != nil || metadata.Permit.Database.DatabaseID != g.scope.DatabaseID || metadata.Scope.CredentialVersion != g.scope.CredentialVersion || metadata.Scope.DeclarationSHA256 != g.scope.DeclarationSHA256 || metadata.Scope.TargetSHA256 != g.scope.TargetSHA256 || metadata.Permit.Replica.BindingID != g.scope.BindingID || metadata.Permit.Replica.EpochID != g.scope.EpochID {
		return errors.Join(errSQLitePrepareHost, err)
	}
	g.unreleased = &metadata
	g.permit, g.definitions = metadata.Permit, metadata.Definitions
	if err = admitSQLiteRoot(ctx, g.host.state, g.permit.Database); err != nil {
		return err
	}
	if err = g.authorizeUnreleased(ctx, true); err != nil {
		return err
	}
	if err = g.unreleasedQuiescent(ctx); err != nil {
		return err
	}
	if g.input.Kind == sqliteprep.AbandonTask {
		return nil
	}
	deadline, ok := ctx.Deadline()
	if !ok || !time.Now().Before(deadline) {
		return errSQLitePrepareHost
	}
	g.requiredDeadline = deadline
	_, err = g.unreleasedCredentials(ctx)
	return err
}

func (g *sqliteHostGuard) authorizeUnreleased(ctx context.Context, bind bool) error {
	if g.host.policy == nil || g.host.identity == nil || g.unreleased == nil {
		return errSQLitePrepareHost
	}
	pol, err := g.host.policy.Load(ctx)
	if err != nil {
		return err
	}
	identity, err := g.host.identity(ctx)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(identity)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	host := "sha256:" + hex.EncodeToString(sum[:])
	destination, ok := pol.BackupDestination(g.permit.Replica.Destination.Reference)
	if host != g.unreleased.DeclarationTargetSHA256 || host != g.unreleased.DeliveryTargetSHA256 || !ok || destination != g.permit.Replica.Destination || !slices.Contains(pol.PersistentRoots(), string(g.permit.Database.Root)) {
		return errSQLitePrepareHost
	}
	if g.input.Kind != sqliteprep.AbandonTask && strings.TrimPrefix(pol.Hash(), "sha256:") != g.scope.PolicySHA256 {
		return errSQLitePrepareHost
	}
	if bind {
		return g.guard.BindUnreleasedAuthority(pol, host)
	}
	return nil
}

func (g *sqliteHostGuard) unreleasedQuiescent(ctx context.Context) error {
	if err := g.authorizeUnreleased(ctx, false); err != nil {
		return err
	}
	for _, file := range []string{
		filepath.Join(g.host.home, ".config/containers/systemd", g.scope.App+".container"),
		filepath.Join(g.host.home, ".config/systemd/user", g.appUnit()),
		filepath.Join(g.host.home, ".config/systemd/user", g.replicaUnit()),
	} {
		if _, err := os.Lstat(file); !errors.Is(err, os.ErrNotExist) {
			return errSQLitePrepareHost
		}
	}
	for _, unit := range []string{g.appUnit(), g.replicaUnit()} {
		if err := g.absentUnreleasedUnit(ctx, unit); err != nil {
			return err
		}
	}
	for _, operation := range g.unreleased.ProducerOperations {
		observed, err := showUnit(ctx, g.host.session, "brine-op-"+operation+".service")
		if err != nil || !observed.stopped(true) && !observed.stopped(false) {
			return errSQLitePrepareHost
		}
	}
	if !containersOutside(ctx, g.host.session, []string{g.sourceDirectory()}) {
		return errSQLitePrepareHost
	}
	return nil
}

func (g *sqliteHostGuard) absentUnreleasedUnit(ctx context.Context, name string) error {
	observed, err := showUnit(ctx, g.host.session, name)
	if err != nil || observed.Load != "not-found" || observed.Active != "inactive" || observed.Sub != "dead" || !observed.stopped(true) {
		return errSQLitePrepareHost
	}
	return nil
}

func (g *sqliteHostGuard) observeUnreleased(ctx context.Context) (sqliteprep.Observation, error) {
	if err := g.unreleasedQuiescent(ctx); err != nil {
		return sqliteprep.Observation{}, err
	}
	schema, err := g.observeSchema(ctx)
	return sqliteprep.Observation{Owned: true, WritersStopped: true, JobsAbsent: true, Replica: sqliteprep.Missing, Schema: schema}, err
}

func (g *sqliteHostGuard) unreleasedCredentials(ctx context.Context) (restore.Credentials, error) {
	if g.input.Kind == sqliteprep.AbandonTask || !time.Now().Before(g.requiredDeadline) {
		return restore.Credentials{}, errSQLitePrepareHost
	}
	if err := g.authorizeUnreleased(ctx, false); err != nil {
		return restore.Credentials{}, err
	}
	metadata, err := g.host.state.ReadSQLiteUnreleasedMetadata(ctx, g.input)
	if err != nil || metadata.Credential.Version != g.scope.CredentialVersion || metadata.DeclarationTargetSHA256 != g.unreleased.DeclarationTargetSHA256 || metadata.Scope.DeclarationSHA256 != g.scope.DeclarationSHA256 {
		return restore.Credentials{}, errors.Join(errSQLitePrepareHost, err)
	}
	record := metadata.Credential
	if record.ExpiresAt != nil && !g.requiredDeadline.Before(*record.ExpiresAt) {
		return restore.Credentials{}, errSQLitePrepareHost
	}
	return readDeliveredCredentials(g.host.stateRoot, record)
}

func (g *sqliteHostGuard) captureBytes(ctx context.Context, id string) ([]byte, error) {
	if g.scope.EmptyAllocation == nil {
		return captureStoppedSQLite(ctx, g.host.stateRoot, "sqlite-prepare-snapshots", g.permit.Database, id)
	}
	if err := ensurePrivateChild(g.host.stateRoot, "sqlite-prepare-snapshots"); err != nil {
		return nil, err
	}
	return restore.EmptySnapshotBytes(ctx, filepath.Join(g.host.stateRoot, "sqlite-prepare-snapshots"), id)
}

func observeSQLiteEmptyCandidate(ctx context.Context, b data.SQLiteDatabaseBinding) (data.SQLiteSchemaObservation, error) {
	source, err := openSQLiteSource(b, nil, nil, nil)
	if err != nil {
		return data.SQLiteSchemaObservation{}, err
	}
	defer func() { _ = source.Close() }()
	if err = ctx.Err(); err != nil {
		return data.SQLiteSchemaObservation{}, err
	}
	if err = source.emptyCensus(); err != nil {
		return data.SQLiteSchemaObservation{}, err
	}
	return data.SQLiteSchemaObservation{State: data.AllocatedEmpty, DatabaseID: b.DatabaseID, Marker: data.EmptyMarker, CatalogSHA256: data.EmptyCatalogSHA256, ObservedAt: time.Now().UTC()}, nil
}
