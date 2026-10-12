package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/target"
)

type PublicationReviewRequest struct {
	App                    string
	PreparedPointID        string
	TargetDeploymentPlanID string
	Artifact               datapublish.Artifact
	RecoveryReleaseIDs     []string
}

type publicationReviewDeclaration struct {
	request  PublicationReviewRequest
	plan     plan.Plan
	desired  policy.Desired
	facts    datapublish.BindingFacts
	recovery datapublish.RecoveryReview
	bundle   NativePreparedBundle
}

func readPublicationReviewDeclarationTx(ctx context.Context, tx *sql.Tx, cap *PublicationReviewPreparation, point nativePreparedPoint, request PublicationReviewRequest) (publicationReviewDeclaration, error) {
	var declaration publicationReviewDeclaration
	planned, desired, err := loadPlan(ctx, tx, request.TargetDeploymentPlanID)
	if err != nil || !publicationDeclarationMatches(planned, desired, point, cap) {
		return declaration, errors.Join(ErrConflict, err)
	}
	bundle, err := readNativePreparedBundleTx(ctx, tx, point.view.PointID)
	if err != nil {
		return declaration, err
	}
	permit, err := readEnginePermit(ctx, tx, point.view.BindingID)
	image := planned.Managed.EngineImage
	if err != nil || image.Digest != strings.SplitN(permit.Runtime.Image, "@", 2)[1] || image.ManifestDigest.Status != target.KnownStatus || image.ManifestDigest.Value == nil || *image.ManifestDigest.Value != permit.Runtime.ManifestDigest {
		return declaration, errors.Join(ErrConflict, err)
	}
	material, err := readNativeRepositoryMaterial(ctx, tx, bundle.transport)
	if err != nil {
		return declaration, err
	}
	runtime := permit.Runtime
	targetRaw, err := json.Marshal(planned.Target)
	if err != nil {
		return declaration, ErrInvalid
	}
	facts := datapublish.BindingFacts{App: cap.app, IncarnationID: string(point.view.IncarnationID), DatabaseID: string(point.view.DatabaseID), BindingID: string(point.view.BindingID), EpochID: string(point.view.EpochID), Generation: point.view.Generation,
		TargetSHA256: publicationStorageSHA256(targetRaw), PolicySHA256: cap.policySHA, DesiredSHA256: strings.TrimPrefix(planned.DesiredHash, "sha256:"), ConfigSHA256: point.view.ConfigSHA256,
		SecretVersions: datapublish.SecretReferences{Publisher: runtime.Secrets.Publisher, SigningPrivate: runtime.Secrets.SigningPrivate, SigningPublic: runtime.Secrets.SigningPublic, AuthPolicy: runtime.Secrets.AuthPolicy,
			RecoveryPassword: data.EngineSecretVersion{Reference: material.RecoveryReference, Version: material.Scope.RecoveryVersion, SHA256: material.RecoverySHA256}}}
	recovery, err := readPublicationRecoveryTx(ctx, tx, cap.app, point.view, desired, request.RecoveryReleaseIDs)
	if err != nil {
		return declaration, err
	}
	request.RecoveryReleaseIDs = slices.Clone(request.RecoveryReleaseIDs)
	return publicationReviewDeclaration{request: request, plan: planned, desired: desired, facts: facts, recovery: recovery, bundle: bundle}, nil
}

func publicationDeclarationMatches(planned plan.Plan, desired policy.Desired, point nativePreparedPoint, cap *PublicationReviewPreparation) bool {
	if planned.App != cap.app || string(desired.Name) != cap.app || planned.Managed == nil || planned.Managed.Database.BindingID != point.view.BindingID || planned.Managed.Database.DatabaseID != point.view.DatabaseID || planned.Managed.Database.IncarnationID != point.view.IncarnationID || planned.Managed.Archive.EpochID != point.view.EpochID || strings.TrimPrefix(planned.PolicyHash, "sha256:") != cap.policySHA || desired.SpacetimeDB == nil || desired.SpacetimeDB.Module.Metadata == nil {
		return false
	}
	for _, conflict := range planned.Conflicts {
		if conflict.Code != plan.ManagedPublicationRequired || conflict.Field != "databases.module" {
			return false
		}
	}
	metadata := desired.SpacetimeDB.Module.Metadata
	return string(metadata.WasmSHA256) == string(cap.artifact.WasmSHA256()) && string(metadata.RuntimeModuleHash) == string(cap.artifact.ProgramWireHash())
}

func readPublicationRecoveryTx(ctx context.Context, tx *sql.Tx, app string, point NativePreparedPointView, target policy.Desired, releaseIDs []string) (datapublish.RecoveryReview, error) {
	if len(releaseIDs) == 0 {
		if point.SourceKind != "unpublished" || publicationIncarnationHasRelease(ctx, tx, app, point) != nil {
			return datapublish.RecoveryReview{}, ErrConflict
		}
		return datapublish.NoPriorReleaseReview(), nil
	}
	if len(releaseIDs) > 16 || !slices.IsSorted(releaseIDs) {
		return datapublish.RecoveryReview{}, ErrInvalid
	}
	refs := make([]datapublish.RecoveryReleaseReference, 0, len(releaseIDs))
	for _, id := range releaseIDs {
		ref, err := readPublicationRecoveryReleaseTx(ctx, tx, app, point, target, id)
		if err != nil {
			return datapublish.RecoveryReview{}, err
		}
		refs = append(refs, ref)
	}
	return datapublish.SelectedRecoveryReleases(refs)
}

func readPublicationRecoveryReleaseTx(ctx context.Context, tx *sql.Tx, app string, point NativePreparedPointView, target policy.Desired, id string) (datapublish.RecoveryReleaseReference, error) {
	release, err := readRelease(ctx, tx, app, id)
	if err != nil || release.Managed == nil || release.Managed.BindingID != point.BindingID {
		return datapublish.RecoveryReleaseReference{}, errors.Join(ErrConflict, err)
	}
	planned, desired, err := loadPlan(ctx, tx, release.PlanID)
	if err != nil || planned.Managed == nil || planned.Managed.Database.IncarnationID != point.IncarnationID || desired.SpacetimeDB == nil || !slices.Contains(desired.SpacetimeDB.Module.Accepts, target.SpacetimeDB.Module.Marker) {
		return datapublish.RecoveryReleaseReference{}, errors.Join(ErrConflict, err)
	}
	_, code, err := normalizeRelease(release)
	if err != nil {
		return datapublish.RecoveryReleaseReference{}, err
	}
	compatibility, err := json.Marshal(desired.SpacetimeDB.Module.Accepts)
	if err != nil {
		return datapublish.RecoveryReleaseReference{}, ErrInvalid
	}
	return datapublish.RecoveryReleaseReference{ReleaseID: id, CodeSHA256: publicationStorageSHA256(code), CompatibilitySHA256: publicationStorageSHA256(compatibility), AcceptedMarker: target.SpacetimeDB.Module.Marker, AcceptedSchemaSHA256: target.SpacetimeDB.Module.Metadata.SchemaSHA256}, nil
}

func publicationIncarnationHasRelease(ctx context.Context, tx *sql.Tx, app string, point NativePreparedPointView) error {
	rows, err := tx.QueryContext(ctx, "SELECT id FROM releases WHERE app=?", app)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil || len(ids) >= 1024 {
			_ = rows.Close()
			return errors.Join(ErrConflict, err)
		}
		ids = append(ids, id)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, id := range ids {
		release, err := readRelease(ctx, tx, app, id)
		if err != nil {
			return err
		}
		if release.Managed == nil || release.Managed.BindingID == point.BindingID {
			return ErrConflict
		}
	}
	return nil
}
