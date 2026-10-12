//go:build linux

package host

import (
	"context"
	"os"
	"strings"

	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/nativeevidence"
)

// readCheckpointPublicationArtifact validates bytes from an already owned copy.
// The stopped checkpoint and passive-reader capabilities are checked by its
// caller; this helper supplies no source or descriptor execution authority.
func readCheckpointPublicationArtifact(ctx context.Context, root *os.Root, checkpoint nativecapture.CheckpointRecord, program nativeevidence.Program) (datapublish.Artifact, error) {
	if root == nil || ctx.Err() != nil || program.Kind != "wasm" || program.Bytes == 0 || program.Bytes > datapublish.ModuleArtifactLimit || program.SHA256 != checkpoint.ArtifactSHA256 || program.Wire != checkpoint.RuntimeModuleHash {
		return datapublish.Artifact{}, datapublish.ErrRecord
	}
	member, err := checkpointProgramMember(checkpoint.Members, program)
	if err != nil {
		return datapublish.Artifact{}, err
	}
	raw, err := readEnginePrivateFile(ctx, root, member.Path, program.SHA256, datapublish.ModuleArtifactLimit)
	if err != nil {
		return datapublish.Artifact{}, datapublish.ErrRecord
	}
	defer clear(raw)
	artifact, err := datapublish.NewArtifact(raw)
	if err != nil || uint64(len(raw)) != member.Bytes || string(artifact.WasmSHA256()) != program.SHA256 || string(artifact.ProgramWireHash()) != program.Wire {
		return datapublish.Artifact{}, datapublish.ErrRecord
	}
	return artifact, nil
}

func checkpointProgramMember(members []nativecapture.CheckpointMember, program nativeevidence.Program) (nativecapture.CheckpointMember, error) {
	var selected nativecapture.CheckpointMember
	for _, member := range members {
		if !strings.HasPrefix(member.Path, "program-bytes/") || member.SHA256 != program.SHA256 {
			continue
		}
		if selected.Path != "" || member.Bytes != program.Bytes {
			return nativecapture.CheckpointMember{}, datapublish.ErrRecord
		}
		selected = member
	}
	if selected.Path == "" {
		return nativecapture.CheckpointMember{}, datapublish.ErrRecord
	}
	return selected, nil
}
