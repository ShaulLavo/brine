//go:build linux

package host

import (
	"context"

	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/spacetimedb"
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
