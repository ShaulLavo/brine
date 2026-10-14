package store

import "strings"

type publicationProofKind string

const (
	publicationDescriptorProof           publicationProofKind = "descriptor"
	publicationRestoreProof              publicationProofKind = "staging_restore"
	publicationPreflightProof            publicationProofKind = "preflight"
	publicationReceiptProof              publicationProofKind = "publication_receipt"
	publicationVerificationProof         publicationProofKind = "target_verification"
	publicationCheckpointProof           publicationProofKind = "target_checkpoint"
	publicationMaterialProof             publicationProofKind = "target_material"
	publicationRecoveryProof             publicationProofKind = "recovery_release"
	publicationDependenciesProof         publicationProofKind = "retained_dependencies"
	publicationDisposalProof             publicationProofKind = "staging_disposal"
	publicationCompletionProof           publicationProofKind = "completion"
	publicationGuestReviewProof          publicationProofKind = "guest_review"
	publicationSchemaProof               publicationProofKind = "schema_requirements"
	publicationPreservationProof         publicationProofKind = "source_preservation"
	publicationTransitionTargetProof     publicationProofKind = "transition_target"
	publicationTransitionCompletionProof publicationProofKind = "transition_completion"
)

func publicationProofKinds() []publicationProofKind {
	return []publicationProofKind{publicationDescriptorProof, publicationRestoreProof, publicationPreflightProof, publicationReceiptProof, publicationVerificationProof, publicationCheckpointProof, publicationMaterialProof, publicationRecoveryProof, publicationDependenciesProof, publicationDisposalProof, publicationCompletionProof, publicationGuestReviewProof, publicationSchemaProof, publicationPreservationProof, publicationTransitionTargetProof, publicationTransitionCompletionProof}
}

func publicationProofKindConstraint() string {
	kinds := publicationProofKinds()
	quoted := make([]string, len(kinds))
	for i, kind := range kinds {
		quoted[i] = "'" + string(kind) + "'"
	}
	return strings.Join(quoted, ",")
}
