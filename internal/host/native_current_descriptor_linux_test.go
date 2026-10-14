//go:build linux

package host

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/store"
)

func TestCurrentDescriptorUsesSuccessorPermitAndExactCurrentArtifact(t *testing.T) {
	permit, _ := engineStartupFixture(t)
	artifact := publicationArtifactFixture(t)
	permit.Runtime.Module.Wasm = data.WasmSHA256(artifact.WasmSHA256())
	permit.Runtime.Module.Program = data.NativeProgramWireHash(artifact.ProgramWireHash())
	original := permit.Runtime.EpochID
	permit.Runtime.EpochID = data.ReplicaEpochID(strings.Repeat("e", 32))
	if permit.Runtime.EpochID == original {
		t.Fatal("fixture did not change epoch")
	}
	if !currentDescriptorSourceMatches(permit, permit, artifact) {
		t.Fatal("successor current artifact required original backup epoch")
	}
	for _, change := range []func(*store.EnginePermit){
		func(p *store.EnginePermit) { p.Runtime.EpochID = original },
		func(p *store.EnginePermit) { p.Runtime.State = data.EngineUnpublished },
		func(p *store.EnginePermit) { p.Runtime.BindingID = data.ReplicaBindingID(strings.Repeat("f", 32)) },
		func(p *store.EnginePermit) { p.Runtime.Module.Wasm = data.WasmSHA256(strings.Repeat("f", 64)) },
		func(p *store.EnginePermit) {
			p.Runtime.Module.Program = data.NativeProgramWireHash(strings.Repeat("f", 64))
		},
		func(p *store.EnginePermit) { p.Runtime.Module.Schema.SHA256 = strings.Repeat("f", 64) },
		func(p *store.EnginePermit) { p.Runtime.ImageID = strings.Repeat("f", 64) },
		func(p *store.EnginePermit) { p.Runtime.Secrets.Publisher.Version++ },
		func(p *store.EnginePermit) { p.Reserved.Database.Root = "/foreign-owned-root" },
	} {
		crossed := permit
		crossed.Runtime = permit.Runtime.Clone()
		change(&crossed)
		if currentDescriptorSourceMatches(crossed, permit, artifact) {
			t.Fatal("foreign or stale source descriptor permit accepted")
		}
	}
	if currentDescriptorSourceMatches(permit, permit, datapublish.Artifact{}) {
		t.Fatal("missing artifact accepted")
	}
	changed := permit
	changed.Runtime = permit.Runtime.Clone()
	changed.Runtime.Module.Wasm = data.WasmSHA256(strings.Repeat("f", 64))
	if currentDescriptorSourceMatches(changed, changed, artifact) {
		t.Fatal("current permit accepted another program artifact")
	}
}

func TestCurrentDescriptorLeaseRefusesAbsentPreparationBeforeEffects(t *testing.T) {
	permit, _ := engineStartupFixture(t)
	if _, err := acquireCurrentDescriptorLease(context.Background(), nil, nil, permit, publicationArtifactFixture(t), nil); !errors.Is(err, datapublish.ErrRecord) {
		t.Fatal("unheld source acquired current descriptor", err)
	}
}
