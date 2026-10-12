//go:build linux

package host

import (
	"context"
	"slices"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/podman"
	"github.com/ShaulLavo/brine/internal/strictjson"
	"github.com/ShaulLavo/brine/internal/target"
)

type nativeSourceDescriptor struct {
	source  engineSealedCheckpoint
	witness nativeLocalWitness
	lease   *descriptorLease
}

type nativeSourceSchema struct {
	source     engineSealedCheckpoint
	witness    nativeLocalWitness
	candidate  datapublish.Candidate
	descriptor []byte
	schemaV10  []byte
	extraction extractedPublicationCandidate
}

func acquireNativeSourceDescriptor(ctx context.Context, source engineSealedCheckpoint, witness nativeLocalWitness, images *podman.Client) (*nativeSourceDescriptor, error) {
	if source.unchanged(ctx) != nil || !witness.matches(source) {
		return nil, datapublish.ErrRecord
	}
	permit, err := source.state.ReadNativeStoppedPermit(ctx, source.stopped)
	if err != nil || permit.Runtime.State != data.EngineInitialized {
		return nil, datapublish.ErrRecord
	}
	preparation, err := source.state.ReadNativeStoppedDescriptorPreparation(ctx, source.stopped)
	if err != nil {
		return nil, datapublish.ErrRecord
	}
	platform, err := descriptorRuntimePlatform(permit.Runtime)
	if err != nil {
		return nil, err
	}
	image, err := descriptorImage(ctx, images, platform)
	if err != nil || image.ManifestDigest != permit.Runtime.ManifestDigest || image.ImageID != permit.Runtime.ImageID {
		return nil, datapublish.ErrRecord
	}
	artifact, err := readCheckpointPublicationArtifact(ctx, source.copy.root, source.checkpoint.Record(), witness.evidence.Program())
	if err != nil || source.unchanged(ctx) != nil {
		return nil, datapublish.ErrRecord
	}
	staged, err := stagePublicationArtifact(ctx, preparation, permit.Runtime.BindingID, artifact)
	if err != nil {
		return nil, err
	}
	lease := &descriptorLease{artifact: staged, image: image, imageRef: permit.Runtime.Image}
	return &nativeSourceDescriptor{source: source, witness: witness, lease: lease}, nil
}

func descriptorRuntimePlatform(runtime data.EngineRuntime) (target.Platform, error) {
	if runtime.Image != data.SpacetimeDBImage || runtime.Version != "2.11.0" {
		return target.Platform{}, datapublish.ErrRecord
	}
	switch runtime.ManifestDigest {
	case data.SpacetimeDBAMD64Manifest:
		return target.Platform{OS: "linux", Arch: "amd64"}, nil
	case data.SpacetimeDBARM64Manifest:
		return target.Platform{OS: "linux", Arch: "arm64"}, nil
	default:
		return target.Platform{}, datapublish.ErrRecord
	}
}

// extract retains its consumed lease on an unknown process outcome. Its caller
// may inspect that attempt; it cannot rerun extraction or discard its artifact.
func (d *nativeSourceDescriptor) extract(ctx context.Context, runner localexec.InputRunner, session localexec.Session) (nativeSourceSchema, error) {
	if d == nil || d.lease == nil || d.source.unchanged(ctx) != nil || !d.witness.matches(d.source) {
		return nativeSourceSchema{}, datapublish.ErrRecord
	}
	extraction, err := extractPublicationCandidate(ctx, d.lease, runner, session)
	if err != nil || d.source.unchanged(ctx) != nil || !d.witness.matches(d.source) {
		return nativeSourceSchema{}, datapublish.ErrRecord
	}
	candidate, descriptor := extraction.candidate, extraction.descriptor
	expected := d.source.checkpoint.Record()
	if candidate.WasmSHA256() != expected.ArtifactSHA256 || candidate.ProgramWireHash() != expected.RuntimeModuleHash || candidate.Schema().SHA256() != expected.SchemaSHA256 || candidate.Schema().Version() != expected.SchemaVersion {
		return nativeSourceSchema{}, datapublish.ErrRecord
	}
	fields, err := strictjson.Object(descriptor, "V10")
	if err != nil {
		return nativeSourceSchema{}, datapublish.ErrRecord
	}
	return nativeSourceSchema{source: d.source, witness: d.witness, candidate: candidate, descriptor: slices.Clone(descriptor), schemaV10: slices.Clone(fields["V10"]), extraction: extraction}, nil
}

func (s nativeSourceSchema) unchanged(ctx context.Context) error {
	if s.source.unchanged(ctx) != nil || !s.witness.matches(s.source) || len(s.descriptor) == 0 || len(s.schemaV10) == 0 || s.extraction.unchanged(ctx) != nil || !slices.Equal(s.descriptor, s.extraction.descriptor) {
		return datapublish.ErrRecord
	}
	fields, err := strictjson.Object(s.descriptor, "V10")
	if err != nil || !slices.Equal(fields["V10"], s.schemaV10) {
		return datapublish.ErrRecord
	}
	r := s.source.checkpoint.Record()
	if s.candidate.WasmSHA256() != r.ArtifactSHA256 || s.candidate.ProgramWireHash() != r.RuntimeModuleHash || s.candidate.Schema().SHA256() != r.SchemaSHA256 || s.candidate.Schema().Version() != r.SchemaVersion {
		return datapublish.ErrRecord
	}
	return nil
}
