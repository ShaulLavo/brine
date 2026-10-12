//go:build linux

package host

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/store"
)

func (e initialPublisherEvidence) unchanged(ctx context.Context) error {
	lease := e.lease
	if lease == nil || lease.restored != nil || lease.empty.copy != e.source.copy || lease.empty.stopped != e.source.stopped || !lease.attempted || !lease.disposed || lease.process != nil || lease.containerID != "" || lease.helperPID == 0 || lease.helperTicks == 0 || lease.helperCgroup == "" || !lease.observation.ChildBound || !lease.observation.ChildStopped || e.source.unchanged(ctx) != nil || lease.sourceUnchanged(ctx) != nil {
		return datapublish.ErrRecord
	}
	if e.pointID != e.source.stopped.PointID() || e.operationID != e.source.stopped.OperationID() || e.sourceSHA != e.source.source.SHA256() || e.stageRoot != lease.rootMember || !publicationStageRootUnchanged(e.source.copy.root, e.source.copy.path, e.stageRoot) || !data.ValidCatalogHash(e.publisher) || e.credentialSHA != e.source.permit.Runtime.Secrets.Publisher.SHA256 {
		return datapublish.ErrRecord
	}
	actual, err := json.Marshal(e.source.permit.Runtime)
	retained, retainedErr := json.Marshal(e.runtime)
	if err != nil || retainedErr != nil || !slices.Equal(actual, retained) {
		return datapublish.ErrRecord
	}
	observation := moduleStageObservation{Action: moduleStagePublisher, Publisher: e.publisher, CredentialSHA256: e.credentialSHA, PublisherProof: e.publisherProof}
	if lease.observation.Action != observation.Action || lease.observation.Publisher != observation.Publisher || lease.observation.CredentialSHA256 != observation.CredentialSHA256 || !slices.Equal(lease.observation.PublisherProof, observation.PublisherProof) {
		return datapublish.ErrRecord
	}
	return checkModuleStagePublisherProof(observation, lease.startedAt, lease.finishedAt)
}

func (e initialPublisherEvidence) provenanceBytes(ctx context.Context) ([]byte, error) {
	if e.unchanged(ctx) != nil || e.source.permit.Allocation == nil {
		return nil, datapublish.ErrRecord
	}
	empty, valid := e.source.source.Empty()
	if !valid {
		return nil, datapublish.ErrRecord
	}
	lease, child := e.lease, e.lease.observation
	return json.Marshal(store.NativeInitialOwnerObservation{Format: "brine-native-initial-owner-provenance-v1", PointID: e.pointID, OperationID: e.operationID,
		BindingID: e.runtime.BindingID, EpochID: e.runtime.EpochID, GenerationID: empty.GenerationID, SourceSHA256: e.sourceSHA, SourceSealSHA256: e.source.original.hash,
		AllocationID: e.source.permit.Allocation.DatabaseID, Runtime: e.runtime.Clone(), StageDevice: e.stageRoot.Device, StageInode: e.stageRoot.Inode,
		HelperPID: lease.helperPID, HelperStartTicks: lease.helperTicks, HelperCgroup: lease.helperCgroup, ChildPID: child.ChildPID, ChildStartTicks: child.ChildStartTicks,
		ChildBound: child.ChildBound, ChildStopped: child.ChildStopped, HelperDisposed: lease.disposed, Publisher: e.publisher, CredentialSHA256: e.credentialSHA,
		PublisherProofSHA256: managedServiceHash(e.publisherProof), PublisherProof: slices.Clone(e.publisherProof), StartedAt: lease.startedAt, FinishedAt: lease.finishedAt})
}

func (initialPublisherEvidence) String() string               { return "[private initial publisher evidence]" }
func (initialPublisherEvidence) GoString() string             { return "[private initial publisher evidence]" }
func (initialPublisherEvidence) MarshalJSON() ([]byte, error) { return nil, datapublish.ErrRecord }
