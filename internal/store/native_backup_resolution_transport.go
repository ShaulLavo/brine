package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/restic"
)

// CompletedTransport exposes only the exact previously authenticated snapshot
// from a real original producer. It allows retrying inspection after cleanup,
// never repeating the upload or constructing a substitute point.
func (i *NativeBackupInspection) CompletedTransport(ctx context.Context) (restic.TransportPoint, bool, error) {
	var zero restic.TransportPoint
	if err := i.check(ctx, i.state.db); err != nil {
		return zero, false, err
	}
	retained, err := i.Upload(ctx)
	if err != nil {
		return zero, false, err
	}
	var pointRaw, sourceRaw, manifestRaw, transportRaw []byte
	point := retained.Manifest.Identity().CaptureID
	if i.original.Kind == ops.NativeCapture {
		err = i.state.db.QueryRowContext(ctx, "SELECT p.canonical,b.source,b.manifest,b.transport FROM data_native_verified_points p JOIN data_native_verified_point_bundles b ON b.point_id=p.id WHERE p.id=? AND p.operation_id=? AND p.binding_id=?", point, i.original.ID, i.binding).Scan(&pointRaw, &sourceRaw, &manifestRaw, &transportRaw)
	} else if i.original.Kind == ops.NativePrepare {
		err = i.state.db.QueryRowContext(ctx, "SELECT p.canonical,b.source,b.manifest,b.transport FROM data_native_prepared_points p JOIN data_native_prepared_point_bundles b ON b.point_id=p.id WHERE p.id=? AND p.binding_id=? AND json_extract(p.canonical,'$.preparation_operation_id')=?", point, i.binding, i.original.ID).Scan(&pointRaw, &sourceRaw, &manifestRaw, &transportRaw)
	} else {
		return zero, false, ErrConflict
	}
	if errors.Is(err, sql.ErrNoRows) {
		return zero, false, nil
	}
	source, _ := json.Marshal(retained.Source)
	if err != nil || !bytes.Equal(source, sourceRaw) || !bytes.Equal(retained.Manifest.Bytes(), manifestRaw) || json.Unmarshal(transportRaw, &zero) != nil {
		return restic.TransportPoint{}, false, &IntegrityError{}
	}
	canonical, _ := json.Marshal(zero)
	if !bytes.Equal(canonical, transportRaw) || zero.Identity != retained.Manifest.Identity() || zero.ManifestSHA256 != retained.Manifest.SHA256() {
		return restic.TransportPoint{}, false, &IntegrityError{}
	}
	if i.original.Kind == ops.NativeCapture {
		v, err := decodeNativeVerifiedPoint(pointRaw)
		if err != nil || v.view.OperationID != i.original.ID || v.view.SourceSHA256 != retained.Source.SHA256() || v.view.ManifestSHA256 != retained.Manifest.SHA256() || v.view.TransportSHA256 != nativePointHash(transportRaw) {
			return restic.TransportPoint{}, false, &IntegrityError{}
		}
	} else {
		v, err := decodeNativePreparedPoint(pointRaw)
		if err != nil || v.view.PreparationOperationID != i.original.ID || v.view.SourceSHA256 != retained.Source.SHA256() || v.view.BundleManifestSHA256 != retained.Manifest.SHA256() || v.view.TransportPointSHA256 != nativePointHash(transportRaw) {
			return restic.TransportPoint{}, false, &IntegrityError{}
		}
	}
	expected := retained.Scope
	expected.CredentialVersion = zero.Scope.CredentialVersion
	if !sameNativeResolutionScope(expected, zero.Scope) {
		return restic.TransportPoint{}, false, ErrConflict
	}
	return zero, true, nil
}

func nativeResolvedTransportMatches(ctx context.Context, q dataQuerier, point, table string, current restic.TransportPoint) ([]byte, error) {
	// The two fixed queries keep SQL identifiers out of caller input.
	var raw []byte
	var err error
	if table == "online" {
		err = q.QueryRowContext(ctx, "SELECT transport FROM data_native_verified_point_bundles WHERE point_id=?", point).Scan(&raw)
	} else if table == "prepared" {
		err = q.QueryRowContext(ctx, "SELECT transport FROM data_native_prepared_point_bundles WHERE point_id=?", point).Scan(&raw)
	} else {
		return nil, ErrConflict
	}
	var original restic.TransportPoint
	if err != nil || json.Unmarshal(raw, &original) != nil {
		return nil, ErrConflict
	}
	canonical, _ := json.Marshal(original)
	if !bytes.Equal(canonical, raw) {
		return nil, &IntegrityError{}
	}
	// Refreshing a read credential/deadline does not change the original snapshot.
	original.Scope = current.Scope
	expected, _ := json.Marshal(original)
	actual, _ := json.Marshal(current)
	if !bytes.Equal(expected, actual) {
		return nil, ErrConflict
	}
	return raw, nil
}
