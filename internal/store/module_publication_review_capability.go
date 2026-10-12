package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datainit"
	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/ops"
)

// PublicationReviewPreparation binds the real completed point, original
// operation and held guard to one owned candidate. It cannot come from decoded
// review syntax, and is not permission to publish or start the original engine.
type PublicationReviewPreparation struct {
	store       *Store
	preparation *EnginePreparation
	pointID     string
	pointSHA    string
	binding     data.ReplicaBindingID
	app         string
	requester   string
	policySHA   string
	artifact    datapublish.Artifact
	declaration *publicationReviewDeclaration
}

func (PublicationReviewPreparation) String() string {
	return "[private publication review preparation]"
}
func (PublicationReviewPreparation) GoString() string {
	return "[private publication review preparation]"
}
func (PublicationReviewPreparation) MarshalJSON() ([]byte, error) { return nil, ErrInvalid }

func (s *Store) beginPublicationReview(ctx context.Context, preparation *EnginePreparation, pointID string, artifact datapublish.Artifact) (*PublicationReviewPreparation, error) {
	if s.readOnly || preparation == nil || preparation.StateRoot() != s.dir || !data.ValidID(pointID) || !data.ValidCatalogHash(string(artifact.WasmSHA256())) {
		return nil, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	point, err := readNativePreparedPointTx(ctx, tx, pointID)
	if err != nil || !preparation.Active(point.view.BindingID) {
		return nil, errors.Join(ErrConflict, err)
	}
	if err = checkNativePointCurrent(ctx, tx, point.view); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if now.Before(point.view.PreparedAt) || !now.Before(point.view.ExpiresAt) || !now.Before(point.view.RetainUntil) {
		return nil, ErrConflict
	}
	op, err := scanOperation(tx.QueryRowContext(ctx, "SELECT id,COALESCE(plan_id,''),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref,recovery_of FROM operations WHERE id=?", point.view.PreparationOperationID))
	if err != nil || op.Kind != ops.NativePrepare || op.State != ops.Succeeded {
		return nil, errors.Join(ErrConflict, err)
	}
	var app, policySHA string
	err = tx.QueryRowContext(ctx, "SELECT app FROM data_active_incarnations WHERE incarnation_id=?", point.view.IncarnationID).Scan(&app)
	if err != nil || app != op.App {
		return nil, errors.Join(ErrConflict, err)
	}
	err = tx.QueryRowContext(ctx, "SELECT policy_hash FROM data_admissions WHERE database_id=? ORDER BY version DESC LIMIT 1", point.view.DatabaseID).Scan(&policySHA)
	if err != nil || !data.ValidCatalogHash(strings.TrimPrefix(policySHA, "sha256:")) {
		return nil, errors.Join(ErrConflict, err)
	}
	capability := &PublicationReviewPreparation{store: s, preparation: preparation, pointID: pointID, pointSHA: point.hash, binding: point.view.BindingID,
		app: app, requester: op.Requester, policySHA: strings.TrimPrefix(policySHA, "sha256:"), artifact: artifact}
	if !capability.active(s) {
		return nil, ErrConflict
	}
	return capability, tx.Commit()
}

func (p *PublicationReviewPreparation) PointID() string                { return p.pointID }
func (p *PublicationReviewPreparation) Artifact() datapublish.Artifact { return p.artifact }

func (s *Store) PreparePublicationReview(ctx context.Context, preparation *EnginePreparation, requester datainit.Requester, request PublicationReviewRequest) (*PublicationReviewPreparation, error) {
	if !requester.Valid() || !validAppName(request.App) || !digestPattern.MatchString(request.TargetDeploymentPlanID) {
		return nil, ErrInvalid
	}
	capability, err := s.beginPublicationReview(ctx, preparation, request.PreparedPointID, request.Artifact)
	if err != nil || capability.requester != requester.String() || capability.app != request.App {
		return nil, errors.Join(ErrConflict, err)
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	point, err := s.readPublicationReviewPreparationTx(ctx, tx, capability)
	if err != nil {
		return nil, err
	}
	declaration, err := readPublicationReviewDeclarationTx(ctx, tx, capability, point, request)
	if err != nil {
		return nil, err
	}
	capability.declaration = &declaration
	return capability, tx.Commit()
}

func (p *PublicationReviewPreparation) active(s *Store) bool {
	return p != nil && p.store == s && !s.readOnly && p.preparation.Active(p.binding) && p.preparation.StateRoot() == s.dir
}

func (s *Store) CheckPublicationReviewPreparation(ctx context.Context, preparation *PublicationReviewPreparation) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = s.readPublicationReviewPreparationTx(ctx, tx, preparation); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) readPublicationReviewPreparationTx(ctx context.Context, tx *sql.Tx, preparation *PublicationReviewPreparation) (nativePreparedPoint, error) {
	if !preparation.active(s) {
		return nativePreparedPoint{}, ErrConflict
	}
	point, err := readNativePreparedPointTx(ctx, tx, preparation.pointID)
	if err != nil || point.hash != preparation.pointSHA || point.view.BindingID != preparation.binding {
		return nativePreparedPoint{}, errors.Join(ErrConflict, err)
	}
	now := time.Now().UTC()
	if now.Before(point.view.PreparedAt) || !now.Before(point.view.ExpiresAt) || !now.Before(point.view.RetainUntil) {
		return nativePreparedPoint{}, ErrConflict
	}
	if err = checkNativePointCurrent(ctx, tx, point.view); err != nil {
		return nativePreparedPoint{}, err
	}
	var requester, app, policySHA string
	err = tx.QueryRowContext(ctx, "SELECT o.requester,a.app FROM operations o JOIN data_active_incarnations a ON a.incarnation_id=? WHERE o.id=? AND o.kind=? AND o.state=?", point.view.IncarnationID, point.view.PreparationOperationID, ops.NativePrepare, ops.Succeeded).Scan(&requester, &app)
	if err != nil || requester != preparation.requester || app != preparation.app {
		return nativePreparedPoint{}, errors.Join(ErrConflict, err)
	}
	err = tx.QueryRowContext(ctx, "SELECT policy_hash FROM data_admissions WHERE database_id=? ORDER BY version DESC LIMIT 1", point.view.DatabaseID).Scan(&policySHA)
	if err != nil || strings.TrimPrefix(policySHA, "sha256:") != preparation.policySHA || !preparation.active(s) {
		return nativePreparedPoint{}, errors.Join(ErrConflict, err)
	}
	return point, nil
}
