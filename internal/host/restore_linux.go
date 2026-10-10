//go:build linux

package host

import (
	"context"
	"database/sql"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/backupcredentials"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/restore"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/store"
)

type restoreTests struct{ stateDir string }

func newRestoreTests(dir string) dispatch.RestoreTestOperations { return restoreTests{stateDir: dir} }

// Restore tests open only the control store read-only. No live source path is
// passed to the engine or the fixed transaction observer.
func (o restoreTests) Test(ctx context.Context, a dispatch.RestoreTestArgs) (restore.Receipt, error) {
	if !a.Valid() {
		return restore.Receipt{}, result.New(result.InvalidUsage, nil)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	state, err := store.OpenReadOnly(ctx, o.stateDir)
	if err != nil {
		return restore.Receipt{}, result.New(result.PolicyRefused, nil)
	}
	defer func() { _ = state.Close() }()
	scopes, err := state.ReadCredentialScopes(ctx, a.App)
	if err != nil {
		return restore.Receipt{}, result.New(result.PolicyRefused, nil)
	}
	scope, err := selectCredentialScope(scopes, a.Database)
	if err != nil || !scope.Replica.Committed {
		return restore.Receipt{}, result.New(result.PolicyRefused, nil)
	}
	declarations, err := state.ReadWriterSchema(ctx, scope.Database.IncarnationID)
	if err != nil {
		return restore.Receipt{}, result.New(result.PolicyRefused, nil)
	}
	request, err := restoreRequest(scope, declarations, a)
	if err != nil {
		return restore.Receipt{}, result.New(result.PolicyRefused, nil)
	}
	if a.Point != "" {
		point, err := state.ReadRestorePoint(ctx, a.Point, scope.Replica.BindingID, scope.Replica.EpochID)
		if err != nil {
			return restore.Receipt{}, result.New(result.PolicyRefused, nil)
		}
		request = requestAtPoint(request, point)
	}
	id, err := data.NewID()
	if err != nil {
		return restore.Receipt{}, result.New(result.DependencyMissing, nil)
	}
	request.OperationID = id
	request.Budget = 9 * time.Minute
	files := backupcredentials.Files{Root: filepath.Join(o.stateDir, "credentials")}
	engine := restore.Engine{Root: filepath.Join(o.stateDir, "restores"), Observer: restoreSchemaObserver{database: scope.Database.Name, definitions: declarations.Definitions}, Bindings: restore.BindingReaderFunc(func(ctx context.Context, id, epoch string) (restore.Binding, error) {
		permit, err := state.ReadReplicaPermitByBinding(ctx, data.ReplicaBindingID(id))
		if err != nil || !permit.Replica.Committed || permit.Replica != scope.Replica || string(permit.Replica.EpochID) != epoch {
			return restore.Binding{}, data.ErrInvalid
		}
		d := scope.Replica.Destination
		return restore.Binding{ID: id, Epoch: epoch, CredentialRef: d.CredentialRef, Destination: restore.Destination{Endpoint: d.Endpoint, Region: d.Region, Bucket: d.Bucket, Prefix: strings.TrimSuffix(scope.Replica.RemotePrefix, "/"), PathStyle: d.PathStyle}}, nil
	}), Credentials: restore.CredentialReaderFunc(func(_ context.Context, ref string) (restore.Credentials, error) {
		if ref != scope.Replica.Destination.CredentialRef {
			return restore.Credentials{}, data.ErrInvalid
		}
		receipt, err := files.Receipt(ref, scope.Replica.CredentialVersion)
		if err != nil || !receipt.Valid() || receipt.Scope.Binding != string(scope.Replica.BindingID) || receipt.Scope.Epoch != string(scope.Replica.EpochID) {
			return restore.Credentials{}, data.ErrInvalid
		}
		packet, err := files.Read(ref, scope.Replica.CredentialVersion)
		if err != nil {
			return restore.Credentials{}, err
		}
		defer packet.Clear()
		credentials := restore.Credentials{ReceivedAt: receipt.ReceivedAt}
		if receipt.ExpiresAt != nil {
			credentials.ExpiresAt = *receipt.ExpiresAt
		}
		for _, entry := range packet.Environment() {
			name, value, _ := strings.Cut(entry, "=")
			switch name {
			case "AWS_ACCESS_KEY_ID":
				credentials.AccessKey = value
			case "AWS_SECRET_ACCESS_KEY":
				credentials.SecretKey = value
			case "AWS_SESSION_TOKEN":
				credentials.SessionToken = value
			}
		}
		return credentials, nil
	})}
	if err := ensurePrivateChild(o.stateDir, "restores"); err != nil {
		return restore.Receipt{}, result.New(result.PolicyRefused, nil)
	}
	receipt, err := engine.Test(ctx, request)
	if err != nil {
		return restore.Receipt{}, result.New(result.PolicyRefused, nil)
	}
	return receipt, nil
}

type restoreSchemaObserver struct {
	database    data.DatabaseName
	definitions []data.SchemaDefinition
}

func (o restoreSchemaObserver) Observe(ctx context.Context, tx *sql.Tx) (restore.SchemaObservation, error) {
	observed := data.ObserveSchemaTransaction(ctx, tx, o.database, o.definitions)
	if observed.State == data.Unknown {
		return restore.SchemaObservation{}, data.ErrInvalid
	}
	return restore.SchemaObservation{State: restore.SchemaState(observed.State), Marker: observed.Marker, CatalogSHA256: observed.CatalogSHA256}, nil
}
func restoreRequest(scope store.CredentialScope, declarations store.WriterSchema, a dispatch.RestoreTestArgs) (restore.Request, error) {

	request := restore.Request{Source: restore.RestoreSource{Kind: restore.LitestreamLTX, LTX: &restore.LTXSource{Recoverability: true, BindingID: string(scope.Replica.BindingID), Epoch: string(scope.Replica.EpochID)}}}
	if a.TXID != "" {
		n, err := strconv.ParseUint(a.TXID, 16, 64)
		if err != nil || n == 0 {
			return restore.Request{}, data.ErrInvalid
		}
		request.Source.LTX.TXID = n
	} else {
		request.Latest = true
	}
	for _, compat := range declarations.Desired.SchemaCompatibility {
		if compat.Database != scope.Database.Name {
			continue
		}
		for _, marker := range compat.Accepts {
			if marker == data.EmptyMarker {
				request.AcceptedSchemas = append(request.AcceptedSchemas, restore.SchemaObservation{State: restore.VerifiedEmpty, Marker: marker, CatalogSHA256: data.EmptyCatalogSHA256})
				continue
			}
			for _, definition := range declarations.Definitions {
				if definition.Database == scope.Database.Name && definition.Marker == marker {
					request.AcceptedSchemas = append(request.AcceptedSchemas, restore.SchemaObservation{State: restore.VerifiedSchema, Marker: marker, CatalogSHA256: definition.CatalogSHA256})
				}
			}
		}
	}
	if len(request.AcceptedSchemas) == 0 {
		return restore.Request{}, data.ErrInvalid
	}
	for _, check := range declarations.Desired.RestoreInvariants {
		if check.Database == scope.Database.Name {
			request.Invariants = append(request.Invariants, restore.Invariant{Kind: restore.InvariantKind(check.Kind), Table: check.Table, Column: check.Column, Count: check.Count, Minimum: check.Minimum, Maximum: check.Maximum})
		}
	}
	return request, nil
}

func requestAtPoint(request restore.Request, point data.RestorePoint) restore.Request {
	request.Latest = false
	request.AcceptedSchemas = nil
	request.ExpectedSchema = restore.SchemaObservation{State: restore.SchemaState(point.Schema.State), Marker: point.Schema.Marker, CatalogSHA256: point.Schema.CatalogSHA256}
	request.Source = restore.RestoreSource{}
	if point.Kind == data.RestorePointSnapshot {
		request.Source.Kind = restore.SQLiteSnapshot
		request.Source.Snapshot = &restore.SnapshotSource{BindingID: string(point.BindingID), Epoch: string(point.EpochID), PointID: point.ID, ObjectKey: point.Snapshot.ObjectKey, SHA256: point.Snapshot.SHA256, Size: point.Snapshot.Size}
	} else {
		request.Source.Kind = restore.LitestreamLTX
		b := point.LTX.Barrier
		request.Source.LTX = &restore.LTXSource{BindingID: string(point.BindingID), Epoch: string(point.EpochID), TXID: point.LTX.TXID, Barrier: &restore.BarrierReceipt{BindingID: string(b.BindingID), Epoch: string(b.EpochID), TXID: b.TXID, ReplicaTXID: b.ReplicaTXID, ObservedAt: b.ObservedAt, Succeeded: true}}
	}
	return request
}
