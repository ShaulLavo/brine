package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"time"

	"github.com/ShaulLavo/brine/internal/nativeevidence"
	"github.com/ShaulLavo/brine/internal/restic"
)

type publicationReviewRestore struct {
	root              *os.Root
	parent            *os.Root
	identity          os.FileInfo
	verifiedAt        time.Time
	sourceEvidenceSHA string
}

// BindPublicationReviewRestore consumes a real exact encrypted download before
// private native boot mutates it. A decoded manifest or evidence hash cannot
// manufacture the opaque authenticated download required by this boundary.
func (s *Store) BindPublicationReviewRestore(ctx context.Context, preparation *PublicationReviewPreparation, restored restic.AuthenticatedRestore, parent *os.Root, evidence nativeevidence.Evidence) error {
	if !preparation.active(s) || preparation.declaration == nil || preparation.restore != nil || parent == nil {
		return ErrConflict
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	point, err := s.readPublicationReviewPreparationTx(ctx, tx, preparation)
	if err != nil {
		return err
	}
	bundle, err := readNativePreparedBundleTx(ctx, tx, point.view.PointID)
	if err != nil || !restored.Matches(ctx, parent, "download", bundle.transport, bundle.manifest) {
		return errors.Join(ErrConflict, err)
	}
	if point.view.SourceKind == "initialized" && (!evidence.SamePersistedState(bundle.sourceEvidence) || evidence.SHA256() != bundle.restoredEvidence.SHA256()) {
		return ErrConflict
	}
	if point.view.SourceKind == "unpublished" && evidence.SHA256() != "" {
		return ErrConflict
	}
	identity, err := restored.Root().Stat(".")
	if err != nil || !identity.IsDir() {
		return errors.Join(ErrConflict, err)
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	preparation.restore = &publicationReviewRestore{root: restored.Root(), parent: parent, identity: identity, verifiedAt: restored.VerifiedAt(), sourceEvidenceSHA: evidence.SHA256()}
	return nil
}

func (r *publicationReviewRestore) unchanged(ctx context.Context) error {
	if r == nil || r.root == nil || r.parent == nil || r.identity == nil || r.verifiedAt.IsZero() || r.verifiedAt.After(time.Now().UTC()) || ctx.Err() != nil {
		return ErrConflict
	}
	held, err := r.root.Stat(".")
	current, parentErr := r.parent.Lstat("download")
	if err != nil || parentErr != nil || !current.IsDir() || !os.SameFile(current, r.identity) || !os.SameFile(held, r.identity) {
		return errors.Join(ErrConflict, err, parentErr)
	}
	return nil
}
