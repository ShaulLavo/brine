//go:build linux

package host

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/nativeevidence"
)

func checkpointArtifactFixture(t *testing.T) (*os.Root, nativecapture.CheckpointRecord, nativeevidence.Program) {
	t.Helper()
	artifact := publicationArtifactFixture(t)
	directory := t.TempDir()
	if err := os.Mkdir(filepath.Join(directory, "program-bytes"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "program-bytes/module.wasm"), artifact.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	program := nativeevidence.Program{Kind: "wasm", Wire: string(artifact.ProgramWireHash()), SHA256: string(artifact.WasmSHA256()), Bytes: uint64(len(artifact.Bytes()))}
	checkpoint := nativecapture.CheckpointRecord{ArtifactSHA256: program.SHA256, RuntimeModuleHash: program.Wire, Members: []nativecapture.CheckpointMember{{Path: "program-bytes/module.wasm", Bytes: program.Bytes, SHA256: program.SHA256}}}
	return root, checkpoint, program
}

func TestPublicationCheckpointArtifactReadsOnlyMatchingNativeProgramBytes(t *testing.T) {
	root, checkpoint, program := checkpointArtifactFixture(t)
	artifact, err := readCheckpointPublicationArtifact(context.Background(), root, checkpoint, program)
	if err != nil || string(artifact.WasmSHA256()) != program.SHA256 || string(artifact.ProgramWireHash()) != program.Wire || !bytes.Equal(artifact.Bytes(), publicationArtifactFixture(t).Bytes()) {
		t.Fatal("actual copied Wasm lost its passive program identity", err)
	}
}

func TestPublicationCheckpointArtifactRefusesMismatchedOrUnboundedSource(t *testing.T) {
	for _, attack := range []string{"wrong-sha", "wrong-wire", "wrong-kind", "zero-bytes", "oversized", "duplicate", "wrong-member-size", "outside-program", "missing", "modified", "symlink", "hardlink", "cancelled"} {
		t.Run(attack, func(t *testing.T) { checkpointArtifactAttack(t, attack) })
	}
}

func checkpointArtifactAttack(t *testing.T, attack string) {
	t.Helper()
	root, checkpoint, program := checkpointArtifactFixture(t)
	ctx := context.Background()
	switch attack {
	case "wrong-sha":
		program.SHA256 = program.Wire
	case "wrong-wire":
		program.Wire = program.SHA256
	case "wrong-kind":
		program.Kind = "js"
	case "zero-bytes":
		program.Bytes = 0
	case "oversized":
		program.Bytes = datapublish.ModuleArtifactLimit + 1
	case "duplicate":
		checkpoint.Members = append(checkpoint.Members, checkpoint.Members[0])
	case "wrong-member-size":
		checkpoint.Members[0].Bytes++
	case "outside-program":
		checkpoint.Members[0].Path = "control-db/module.wasm"
	case "missing":
		checkpoint.Members = nil
	case "modified":
		if err := root.WriteFile("program-bytes/module.wasm", []byte("changed"), 0600); err != nil {
			t.Fatal(err)
		}
	case "symlink":
		if err := root.Symlink("module.wasm", "program-bytes/link.wasm"); err != nil {
			t.Fatal(err)
		}
		checkpoint.Members[0].Path = "program-bytes/link.wasm"
	case "hardlink":
		if err := root.Link("program-bytes/module.wasm", "program-bytes/link.wasm"); err != nil {
			t.Fatal(err)
		}
	case "cancelled":
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		ctx = cancelled
	}
	if _, err := readCheckpointPublicationArtifact(ctx, root, checkpoint, program); !errors.Is(err, datapublish.ErrRecord) {
		t.Fatal("unmatched source bytes supplied a descriptor artifact", attack, err)
	}
}
