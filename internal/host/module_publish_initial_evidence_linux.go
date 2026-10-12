//go:build linux

package host

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datapublish"
)

func (e initialPublisherEvidence) unchanged(ctx context.Context) error {
	lease := e.lease
	if lease == nil || lease.restored != nil || lease.empty.copy != e.source.copy || lease.empty.stopped != e.source.stopped || !lease.attempted || !lease.disposed || lease.process != nil || lease.containerID != "" || e.source.unchanged(ctx) != nil || lease.sourceUnchanged(ctx) != nil {
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
	return checkModuleStagePublisherProof(observation, lease.startedAt, lease.finishedAt)
}

func (initialPublisherEvidence) String() string               { return "[private initial publisher evidence]" }
func (initialPublisherEvidence) GoString() string             { return "[private initial publisher evidence]" }
func (initialPublisherEvidence) MarshalJSON() ([]byte, error) { return nil, datapublish.ErrRecord }
