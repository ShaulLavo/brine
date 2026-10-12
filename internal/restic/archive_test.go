package restic

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testIdentity() Identity {
	return Identity{BindingID: strings.Repeat("a", 32), EpochID: strings.Repeat("b", 32), CaptureID: strings.Repeat("c", 32), ModuleSHA256: strings.Repeat("d", 64), CheckpointSHA256: strings.Repeat("e", 64)}
}

func TestArchiveRefusesPhysicalTailAndCorruptMember(t *testing.T) {
	source, work := testTree(t)
	bundle, err := Seal(context.Background(), source, work, testIdentity(), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bundle.Close() }()
	raw, err := os.ReadFile(bundle.path)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := os.OpenRoot(work)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parent.Close() }()
	for i, attack := range [][]byte{
		append(append([]byte{}, raw...), make([]byte, 512)...),
		raw[:len(raw)-512],
		bytes.Replace(raw, []byte("confirmed fixture state"), []byte("corrupted fixture state"), 1),
	} {
		file := filepath.Join(work, "attack-"+string(rune('a'+i))+".tar")
		// #nosec G703 -- Controlled attack bytes under disposable private test root.
		if err := os.WriteFile(file, attack, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := extractEnvelope(context.Background(), file, parent, "output-"+string(rune('a'+i)), bundle.Manifest(), DefaultLimits()); !errors.Is(err, ErrIntegrity) {
			t.Fatal("unsafe physical archive admitted", err)
		}
	}
}

func testTree(t *testing.T) (string, string) {
	t.Helper()
	base := t.TempDir()
	// #nosec G302 -- Directory mode, not a file permission.
	if err := os.Chmod(base, 0700); err != nil {
		t.Fatal(err)
	}
	source, work := filepath.Join(base, "source"), filepath.Join(base, "work")
	for _, directory := range []string{source, work} {
		if err := os.Mkdir(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(source, "state"), []byte("confirmed fixture state"), 0600); err != nil {
		t.Fatal(err)
	}
	return source, work
}

func TestArchiveRoundTripAndChangedSource(t *testing.T) {
	source, work := testTree(t)
	bundle, err := Seal(context.Background(), source, work, testIdentity(), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bundle.Close() }()
	manifest := bundle.Manifest()
	members := manifest.Members()
	members[0].SHA256 = strings.Repeat("f", 64)
	if manifest.Members()[0].SHA256 == members[0].SHA256 {
		t.Fatal("caller changed sealed evidence")
	}
	output := filepath.Join(work, "restored")
	parent, err := os.OpenRoot(work)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parent.Close() }()
	root, err := extractEnvelope(context.Background(), bundle.path, parent, "restored", manifest, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	// #nosec G304 -- Exact restored member under disposable test directory.
	got, err := os.ReadFile(filepath.Join(output, "state"))
	if err != nil || string(got) != "confirmed fixture state" {
		t.Fatal("restored member differs")
	}
	if err := os.WriteFile(bundle.path, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := bundle.verify(context.Background()); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("changed sealed bundle admitted: %v", err)
	}
}

func TestArchiveRejectsLinksModesAndQuotas(t *testing.T) {
	for _, attack := range []string{"symlink", "hardlink", "mode", "quota"} {
		t.Run(attack, func(t *testing.T) {
			source, work := testTree(t)
			limits := DefaultLimits()
			switch attack {
			case "symlink":
				if err := os.Symlink("state", filepath.Join(source, "link")); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(filepath.Join(source, "state"), filepath.Join(source, "link")); err != nil {
					t.Fatal(err)
				}
			case "mode":
				// #nosec G302 -- Deliberately unsafe fixture permissions must be refused.
				if err := os.Chmod(filepath.Join(source, "state"), 0644); err != nil {
					t.Fatal(err)
				}
			case "quota":
				limits.Bytes = 2
			}
			bundle, err := Seal(context.Background(), source, work, testIdentity(), limits)
			if err == nil {
				_ = bundle.Close()
				t.Fatal("unsafe archive admitted")
			}
		})
	}
}

func TestManifestRejectsUntrustedShapes(t *testing.T) {
	source, work := testTree(t)
	bundle, err := Seal(context.Background(), source, work, testIdentity(), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bundle.Close() }()
	raw := bundle.Manifest().Bytes()
	for _, attack := range []string{
		strings.Replace(string(raw), `"state"`, `"../state"`, 1),
		strings.Replace(string(raw), `"state"`, `"/state"`, 1),
		strings.Replace(string(raw), `"file"`, `"symlink"`, 1),
		strings.Replace(string(raw), `"version":1`, `"version":1,"version":1`, 1),
		string(raw) + `{}`,
	} {
		if _, err := DecodeManifest([]byte(attack), DefaultLimits()); err == nil {
			t.Fatal("unsafe manifest admitted")
		}
	}
}
