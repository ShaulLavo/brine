package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/restic"
)

func readNativeInheritedRepositoryMaterial(ctx context.Context, q dataQuerier, point restic.TransportPoint, archive data.ArchiveBinding, ancestors map[string]bool) (NativeRepositoryMaterial, error) {
	epoch, actualArchive, err := readNativeEpoch(ctx, q, archive.BindingID, archive.EpochID)
	if err != nil || actualArchive != archive || epoch.Record().RepositoryID != point.Scope.RepositoryID {
		return NativeRepositoryMaterial{}, errors.Join(ErrConflict, err)
	}
	transition, cursor, err := readEngineTransition(ctx, q, epoch.Record().TransitionID)
	if err != nil || cursor.Stage != data.EngineTransitionCompleted || !epoch.Matches(transition) {
		return NativeRepositoryMaterial{}, errors.Join(ErrConflict, err)
	}
	common := transition.Common()
	intent, err := readNativeRepositoryIntentByOperation(ctx, q, common.Scope.ApplyOperationID)
	if err != nil || intent.record.OperationID != common.Scope.ApplyOperationID || intent.record.EpochID != archive.EpochID || intent.record.SourceEpochID != epoch.Record().PredecessorEpochID || intent.record.PointID != common.PreparedPoint.ID || intent.record.Archive != archive {
		return NativeRepositoryMaterial{}, errors.Join(ErrConflict, err)
	}
	result, err := readNativeCompletedRepositoryResult(ctx, q, intent)
	if err != nil || result.RepositoryID != point.Scope.RepositoryID || result.Scope.RecoveryVersion != point.Scope.RecoveryVersion {
		return NativeRepositoryMaterial{}, errors.Join(ErrConflict, err)
	}
	bundle, err := readNativePreparedBundleTx(ctx, q, intent.record.PointID)
	if err != nil || bundle.point.hash != common.PreparedPoint.SHA256 || bundle.point.view.RuntimeVersion != common.Base.Head().Version || bundle.point.view.RuntimeSHA256 != common.Base.Head().RuntimeSHA256 || bundle.point.view.EpochID != intent.record.SourceEpochID {
		return NativeRepositoryMaterial{}, errors.Join(ErrConflict, err)
	}
	head := data.EngineHeadRef{BindingID: archive.BindingID, EpochID: archive.EpochID, Version: common.Base.Head().Version + 1, RuntimeSHA256: common.TargetRuntimeSHA256}
	var runtimeRaw []byte
	err = q.QueryRowContext(ctx, "SELECT canonical FROM data_engine_runtime_commits WHERE binding_id=? AND version=?", head.BindingID, head.Version).Scan(&runtimeRaw)
	var runtime data.EngineRuntime
	if err != nil || nativePointHash(runtimeRaw) != head.RuntimeSHA256 || json.Unmarshal(runtimeRaw, &runtime) != nil || runtime.Validate() != nil || runtime.BindingID != head.BindingID || runtime.EpochID != head.EpochID {
		return NativeRepositoryMaterial{}, errors.Join(&IntegrityError{}, err)
	}
	reserved, err := readReservedEngine(ctx, q, archive.BindingID)
	if err != nil {
		return NativeRepositoryMaterial{}, err
	}
	if _, err := readNativeEpochMaterial(ctx, q, head, reserved); err != nil {
		return NativeRepositoryMaterial{}, err
	}
	material, err := readNativeRepositoryMaterialLineage(ctx, q, bundle.transport, ancestors)
	if err != nil || material.Scope.RecoveryVersion != result.Scope.RecoveryVersion || material.CredentialReference != archive.Destination.CredentialRef {
		return NativeRepositoryMaterial{}, errors.Join(ErrConflict, err)
	}
	material.Location = nativeArchiveLocation(archive)
	material.Scope.EpochID = string(archive.EpochID)
	material.Scope.RepositoryID = result.RepositoryID
	material.Scope.DestinationSHA256 = material.Location.SHA256()
	if material.Scope.DestinationSHA256 != point.Scope.DestinationSHA256 {
		return NativeRepositoryMaterial{}, ErrConflict
	}
	return material, nil
}

func readNativeCompletedRepositoryResult(ctx context.Context, q dataQuerier, intent nativeRepositoryIntent) (NativeInitializedRepositoryResult, error) {
	_, lease, _, err := readNativeRepositoryCreationLease(ctx, q, intent)
	if err != nil {
		return NativeInitializedRepositoryResult{}, err
	}
	events, err := readNativeRepositoryInitEvents(ctx, q, intent, nativePointHash(lease))
	if err != nil || len(events) == 0 || events[len(events)-1].Stage != "initialized" {
		return NativeInitializedRepositoryResult{}, errors.Join(ErrConflict, err)
	}
	var repository string
	var raw []byte
	err = q.QueryRowContext(ctx, "SELECT repository_id,canonical FROM data_native_repository_results WHERE intent_id=?", intent.creationID).Scan(&repository, &raw)
	var result NativeInitializedRepositoryResult
	if err != nil || len(raw) > 4096 || json.Unmarshal(raw, &result) != nil {
		return result, errors.Join(&IntegrityError{}, err)
	}
	canonical, err := json.Marshal(result)
	scope := result.Scope
	if err != nil || !bytes.Equal(raw, canonical) || result.IntentID != intent.creationID || result.IntentSHA256 != intent.hash || result.RepositoryID != repository || !data.ValidCatalogHash(repository) || !data.ValidCatalogHash(result.ConfigSHA256) || scope.BindingID != string(intent.record.BindingID) || scope.EpochID != string(intent.epochID) || scope.RepositoryID != repository || scope.DestinationSHA256 != nativeArchiveLocation(intent.archive).SHA256() || scope.CredentialVersion == 0 || scope.RecoveryVersion == 0 || !scope.ExpiresAt.After(result.VerifiedAt) || result.VerifiedAt.Location() != time.UTC || result.VerifiedAt.Before(events[0].ObservedAt) || result.VerifiedAt.After(events[len(events)-1].ObservedAt) {
		return result, &IntegrityError{}
	}
	if err := checkNativeInitializedCredential(ctx, q, intent, result); err != nil {
		return result, err
	}
	return result, nil
}

func checkNativeInitializedCredential(ctx context.Context, q dataQuerier, intent nativeRepositoryIntent, result NativeInitializedRepositoryResult) error {
	var raw []byte
	err := q.QueryRowContext(ctx, "SELECT canonical FROM data_credential_records WHERE binding_id=? AND kind='receipt' AND json_extract(canonical,'$.credential_ref')=? AND json_extract(canonical,'$.version')=?", intent.record.BindingID, intent.archive.Destination.CredentialRef, result.Scope.CredentialVersion).Scan(&raw)
	var record CredentialRecord
	if err != nil || json.Unmarshal(raw, &record) != nil {
		return errors.Join(ErrConflict, err)
	}
	canonical, err := json.Marshal(record)
	if err != nil || !bytes.Equal(raw, canonical) || record.BindingID != intent.record.BindingID || record.CredentialRef != intent.archive.Destination.CredentialRef || record.Destination != intent.archive.Destination.Reference || record.Version != result.Scope.CredentialVersion || record.Kind != "receipt" || record.ReceivedAt.IsZero() || record.ReceivedAt.Location() != time.UTC || record.ReceivedAt.After(result.VerifiedAt) || record.PolicyHash != "sha256:"+intent.record.PolicySHA256 {
		return errors.Join(&IntegrityError{}, err)
	}
	if record.ExpiresAt != nil && (!result.VerifiedAt.Before(*record.ExpiresAt) || result.Scope.ExpiresAt.After(*record.ExpiresAt)) {
		return ErrConflict
	}
	policy, _, err := readNativeBackupPolicyScope(ctx, q, record.BindingID, record.EpochID, "", record.Version, result.Scope.RecoveryVersion)
	if err != nil || policy.CredentialReceiptID != record.ID || checkNativeBackupPolicyOperation(ctx, q, policy) != nil {
		return errors.Join(ErrConflict, err)
	}
	return checkNativeCredentialEpochAncestry(ctx, q, record.BindingID, intent.record.SourceEpochID, record.EpochID)
}

func checkNativeCredentialEpochAncestry(ctx context.Context, q dataQuerier, binding data.ReplicaBindingID, source, delivered data.ReplicaEpochID) error {
	seen := map[data.ReplicaEpochID]bool{}
	for source != delivered {
		if seen[source] {
			return &IntegrityError{}
		}
		seen[source] = true
		epoch, _, err := readNativeEpoch(ctx, q, binding, source)
		if err != nil {
			return err
		}
		transition, cursor, err := readEngineTransition(ctx, q, epoch.Record().TransitionID)
		if err != nil || cursor.Stage != data.EngineTransitionCompleted || !epoch.Matches(transition) {
			return errors.Join(ErrConflict, err)
		}
		source = epoch.Record().PredecessorEpochID
	}
	return nil
}

func nativeArchiveLocation(archive data.ArchiveBinding) restic.S3Location {
	d := archive.Destination
	return restic.S3Location{Endpoint: d.Endpoint, Bucket: d.Bucket, Prefix: strings.TrimSuffix(archive.RemotePrefix, "/"), Region: d.Region, PathStyle: d.PathStyle}
}

func readNativeCurrentRepositoryMaterial(ctx context.Context, q dataQuerier, permit EnginePermit) (NativeRepositoryMaterial, error) {
	material, err := readNativeCurrentRepositoryMaterialRecords(ctx, q, permit)
	if err != nil {
		return NativeRepositoryMaterial{}, err
	}
	return limitNativeRepositoryMaterial(material, material.CredentialRecord.ExpiresAt)
}

func readNativeCurrentRepositoryMaterialRecords(ctx context.Context, q dataQuerier, permit EnginePermit) (NativeRepositoryMaterial, error) {
	policy, _, err := readNativeBackupPolicy(ctx, q, permit.Runtime.BindingID)
	if err != nil {
		return NativeRepositoryMaterial{}, err
	}
	scope := restic.Scope{BindingID: string(permit.Runtime.BindingID), EpochID: string(permit.Runtime.EpochID), RepositoryID: policy.RepositoryID, DestinationSHA256: nativeArchiveLocation(permit.Reserved.Archive).SHA256(), CredentialVersion: policy.CredentialVersion, RecoveryVersion: policy.RecoveryVersion}
	if policy.EpochID != permit.Runtime.EpochID {
		epoch, _, err := readNativeEpoch(ctx, q, permit.Runtime.BindingID, permit.Runtime.EpochID)
		if err != nil {
			return NativeRepositoryMaterial{}, err
		}
		scope.RepositoryID = epoch.Record().RepositoryID
	}
	return readNativeRepositoryMaterialRecords(ctx, q, restic.TransportPoint{Scope: scope})
}

func checkNativeRetainedRepositoryDelivery(ctx context.Context, q dataQuerier, retention nativeRetainedMaterial) error {
	var raw []byte
	err := q.QueryRowContext(ctx, "SELECT transport FROM data_native_verified_point_bundles WHERE point_id=?", retention.PointID).Scan(&raw)
	var point restic.TransportPoint
	if err != nil || json.Unmarshal(raw, &point) != nil || point.Identity.CaptureID != retention.PointID || point.Identity.BindingID != string(retention.BindingID) || point.Scope.EpochID != string(retention.EpochID) || point.ManifestSHA256 != retention.ManifestSHA256 {
		return errors.Join(ErrConflict, err)
	}
	material, err := readNativeRepositoryMaterialRecords(ctx, q, point)
	if err != nil || material.RecoveryDeliveryID != retention.DeliveryID || material.RecoveryReference != retention.Reference || material.Scope.RecoveryVersion != retention.Version || material.RecoverySHA256 != retention.MaterialSHA256 {
		return errors.Join(ErrConflict, err)
	}
	return nil
}
