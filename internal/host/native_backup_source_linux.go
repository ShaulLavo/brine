//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/podman"
	"github.com/ShaulLavo/brine/internal/spacetimedb"
	"github.com/ShaulLavo/brine/internal/store"
	"golang.org/x/sys/unix"
)

type nativeCurrentModule struct {
	artifact   datapublish.Artifact
	descriptor []byte
	candidate  extractedPublicationCandidate
}

func (m nativeCurrentModule) close() error {
	lease := m.candidate.lease
	if lease == nil {
		return nil
	}
	if lease.process != nil || lease.containerID != "" || lease.attempted && !lease.disposed {
		return errEngineSource
	}
	return lease.artifact.discard()
}

func readNativeProgramArtifact(ctx context.Context, root *os.Root, permit store.EnginePermit) (datapublish.Artifact, error) {
	census, err := scanEngineOnlineMetadata(ctx, root, engineCopyLimits{Bytes: 64 << 20, Files: 16384, Depth: 16})
	if err != nil {
		return datapublish.Artifact{}, err
	}
	var selected datapublish.Artifact
	count := 0
	for _, member := range census.members {
		if !strings.HasPrefix(member.Name, "program-bytes/") || member.Mode&unix.S_IFMT != unix.S_IFREG {
			continue
		}
		if member.Size < 8 || member.Size > datapublish.ModuleArtifactLimit {
			return datapublish.Artifact{}, errEngineSource
		}
		raw, err := readNativeProgramMember(ctx, root, member)
		if err != nil {
			return datapublish.Artifact{}, err
		}
		artifact, err := datapublish.NewArtifact(raw)
		if err != nil {
			return datapublish.Artifact{}, errEngineSource
		}
		if string(artifact.WasmSHA256()) == string(permit.Runtime.Module.Wasm) && string(artifact.ProgramWireHash()) == string(permit.Runtime.Module.Program) {
			selected = artifact
			count++
		}
	}
	if count != 1 {
		return datapublish.Artifact{}, errEngineSource
	}
	return selected, nil
}

func readNativeProgramMember(ctx context.Context, root *os.Root, member engineTreeMember) ([]byte, error) {
	file, err := engineOpenMember(root, member.Name, unix.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	hash, err := hashEngineCapturedPrefix(ctx, file, member, member.Size)
	if err != nil {
		return nil, err
	}
	return readEnginePrivateFile(ctx, root, member.Name, hash, datapublish.ModuleArtifactLimit)
}

func (r nativeBackupRuntime) extractCurrentModule(ctx context.Context, preparation *store.EnginePreparation, permit store.EnginePermit, artifact datapublish.Artifact) (module nativeCurrentModule, err error) {
	lease, err := acquireCurrentDescriptorLease(ctx, r.state, preparation, permit, artifact, podman.New(r.session))
	if err != nil {
		return nativeCurrentModule{}, err
	}
	complete := false
	defer func() {
		if !complete && (!lease.attempted || lease.disposed) {
			err = errors.Join(err, lease.artifact.discard())
		}
	}()
	extracted, err := extractPublicationCandidate(ctx, lease, localexec.ExecRunner{}, r.session)
	if err != nil || extracted.unchanged(ctx) != nil || extracted.candidate.Schema().SHA256() != permit.Runtime.Module.Schema.SHA256 {
		return nativeCurrentModule{}, errEngineSource
	}
	complete = true
	return nativeCurrentModule{artifact: artifact, descriptor: extracted.descriptor, candidate: extracted}, nil
}

func observeNativeConfirmed(ctx context.Context, permit store.EnginePermit, material engineMaterial, module nativeCurrentModule) (spacetimedb.ConfirmedPairsRecord, error) {
	r := permit.Runtime
	endpoint, err := spacetimedb.NewPrivateEndpoint("127.0.0.1", r.HostPort, string(r.Database))
	if err != nil {
		return spacetimedb.ConfirmedPairsRecord{}, err
	}
	credential, err := spacetimedb.NewPublisherCredential(material.publisher)
	if err != nil {
		return spacetimedb.ConfirmedPairsRecord{}, err
	}
	expected, err := spacetimedb.NewExpectedDatabase(string(r.Database), string(r.Publisher), r.Names)
	if err != nil {
		return spacetimedb.ConfirmedPairsRecord{}, err
	}
	observed, err := spacetimedb.ObserveConfirmedPairs(ctx, endpoint, credential, expected, module.artifact.Bytes(), module.candidate.candidate.Schema(), spacetimedb.DefaultObserverLimits())
	return observed.Record(), err
}

type nativePublicationArtifactSource interface {
	ReadNativeCurrentPublicationArtifact(context.Context, data.ReplicaBindingID) (datapublish.Artifact, error)
}

func readNativeCurrentArtifact(ctx context.Context, source nativePublicationArtifactSource, root *os.Root, permit store.EnginePermit) (datapublish.Artifact, error) {
	artifact, err := source.ReadNativeCurrentPublicationArtifact(ctx, permit.Runtime.BindingID)
	if errors.Is(err, store.ErrNotFound) {
		return readNativeProgramArtifact(ctx, root, permit)
	}
	if err != nil {
		return datapublish.Artifact{}, err
	}
	if string(artifact.WasmSHA256()) != string(permit.Runtime.Module.Wasm) || string(artifact.ProgramWireHash()) != string(permit.Runtime.Module.Program) {
		return datapublish.Artifact{}, errEngineSource
	}
	return artifact, nil
}
