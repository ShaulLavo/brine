//go:build linux

package host

import (
	"context"

	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/spacetimedb"
	"github.com/ShaulLavo/brine/internal/store"
)

type extractedPublicationCandidate struct {
	lease      *descriptorLease
	candidate  datapublish.Candidate
	descriptor []byte
}

func (c extractedPublicationCandidate) unchanged(ctx context.Context) error {
	lease := c.lease
	if lease == nil || lease.artifact == nil || !lease.attempted || !lease.disposed || lease.process != nil || lease.containerID != "" || lease.artifact.unchanged(ctx) != nil {
		return datapublish.ErrRecord
	}
	artifact := lease.artifact.artifact
	schema, err := spacetimedb.DecodeDescriptorV10(c.descriptor, spacetimedb.DefaultSchemaLimits())
	if err != nil || c.candidate.Schema().SHA256() != schema.SHA256() || c.candidate.WasmSHA256() != string(artifact.WasmSHA256()) || c.candidate.ProgramWireHash() != string(artifact.ProgramWireHash()) {
		return datapublish.ErrRecord
	}
	return nil
}

func (extractedPublicationCandidate) String() string {
	return "[private extracted publication candidate]"
}
func (extractedPublicationCandidate) GoString() string {
	return "[private extracted publication candidate]"
}
func (extractedPublicationCandidate) MarshalJSON() ([]byte, error) { return nil, datapublish.ErrRecord }

func (c extractedPublicationCandidate) provenance(ctx context.Context) (store.PublicationDescriptorObservation, error) {
	if c.unchanged(ctx) != nil || c.lease.helperPID == 0 || c.lease.helperTicks == 0 || c.lease.helperCgroup == "" || c.lease.startedAt.IsZero() || c.lease.finishedAt.Before(c.lease.startedAt) {
		return store.PublicationDescriptorObservation{}, datapublish.ErrRecord
	}
	lease := c.lease
	return store.PublicationDescriptorObservation{Image: lease.imageRef, ManifestDigest: lease.image.ManifestDigest, ImageID: lease.image.ImageID, HelperPID: lease.helperPID, HelperStartTicks: lease.helperTicks,
		HelperCgroup: lease.helperCgroup, StartedAt: lease.startedAt, FinishedAt: lease.finishedAt, Disposed: lease.disposed}, nil
}
