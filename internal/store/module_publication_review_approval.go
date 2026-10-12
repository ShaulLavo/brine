package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"

	"github.com/ShaulLavo/brine/internal/datapublish"
)

func readPublicationReviewApprovalTx(ctx context.Context, tx *sql.Tx, review datapublish.PrivateReview, index publicationReviewBinding) (publicationReviewApproval, error) {
	var raw []byte
	var hash, requester, point, target string
	var approval publicationReviewApproval
	err := tx.QueryRowContext(ctx, "SELECT requester,point_id,target_declaration_id,record_sha256,canonical FROM data_module_publication_approvals WHERE plan_id=?", review.Plan().ID()).Scan(&requester, &point, &target, &hash, &raw)
	if err != nil {
		return approval, err
	}
	if len(raw) == 0 || len(raw) > 4<<20 || publicationStorageSHA256(raw) != hash || json.Unmarshal(raw, &approval) != nil {
		return approval, &IntegrityError{}
	}
	canonical, err := json.Marshal(approval)
	planned := review.Plan()
	if err != nil || !bytes.Equal(canonical, raw) || approval.Format != "brine-publication-review-approval-v1" || approval.PlanID != planned.ID() || requester != index.requester || approval.Requester != requester || point != index.pointID || approval.PointID != point || approval.PointSHA256 != index.pointSHA || approval.TargetDeclarationID != target || !approval.ExpiresAt.Equal(planned.ExpiresAt()) || !bytes.Equal(approval.Observation.Descriptor, review.CandidateDescriptor()) || !bytes.Equal(approval.Observation.Preflight, review.NativePreflightResponse()) {
		return approval, &IntegrityError{}
	}
	return approval, nil
}
