//go:build linux

package host

import (
	"context"
	"os"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datapublish"
)

type completedPublicationArtifact struct{ artifact datapublish.Artifact }

func (p completedPublicationArtifact) ReadNativeCurrentPublicationArtifact(context.Context, data.ReplicaBindingID) (datapublish.Artifact, error) {
	return p.artifact, nil
}

func TestNativePreparationAfterCompletedPublicationUsesCurrentReviewedArtifact(t *testing.T) {
	permit, _ := engineStartupFixture(t)
	old, err := os.ReadFile("../spacetimedb/testdata/recovery-v1.wasm")
	if err != nil {
		t.Fatal(err)
	}
	published := publicationArtifactFixture(t)
	permit.Runtime.Module.Wasm = data.WasmSHA256(published.WasmSHA256())
	permit.Runtime.Module.Program = data.NativeProgramWireHash(published.ProgramWireHash())
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if err = root.Mkdir("program-bytes", 0700); err != nil {
		t.Fatal(err)
	}
	if err = root.WriteFile("program-bytes/bootstrap.wasm", old, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = readNativeProgramArtifact(t.Context(), root, permit); err == nil {
		t.Fatal("fixture cache already contains the published module")
	}
	artifact, err := readNativeCurrentArtifact(t.Context(), completedPublicationArtifact{published}, root, permit)
	if err != nil || artifact.WasmSHA256() != published.WasmSHA256() {
		t.Fatal("preparation after publication still requires bootstrap cache to contain current Wasm", err)
	}
}
