//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/podman"
	"github.com/ShaulLavo/brine/internal/spec"
)

type recordedEngineImageExecutor struct {
	row      map[string]any
	commands [][]string
}

func (r *recordedEngineImageExecutor) Execute(_ context.Context, c localexec.Command) (localexec.Result, error) {
	if c.Mutation || len(c.Args) != 3 || !slices.Equal(c.Args[:2], []string{"image", "inspect"}) {
		return localexec.Result{}, errors.New("unexpected recorded-image effect")
	}
	r.commands = append(r.commands, slices.Clone(c.Args))
	raw, err := json.Marshal([]map[string]any{r.row})
	return localexec.Result{Stdout: string(raw)}, err
}

func TestDescriptorInspectsExactRecordedImageWithoutSelectingCurrentProfile(t *testing.T) {
	for _, attack := range []string{"none", "config", "index", "manifest", "platform"} {
		t.Run(attack, func(t *testing.T) { testRecordedDescriptorImage(t, attack) })
	}
}

func testRecordedDescriptorImage(t *testing.T, attack string) {
	t.Helper()
	permit, _ := engineStartupFixture(t)
	runtime := permit.Runtime
	runtime.Image = "registry.example/retained-engine@sha256:" + strings.Repeat("1", 64)
	runtime.ManifestDigest = "sha256:" + strings.Repeat("2", 64)
	selected := "registry.example/retained-engine@" + runtime.ManifestDigest
	executor := &recordedEngineImageExecutor{row: map[string]any{"Id": runtime.ImageID, "Digest": runtime.ManifestDigest, "RepoDigests": []string{runtime.Image, selected}, "Architecture": "arm64", "Os": "linux", "ManifestType": "application/vnd.oci.image.manifest.v1+json"}}
	switch attack {
	case "config":
		executor.row["Id"] = strings.Repeat("3", 64)
	case "index":
		executor.row["RepoDigests"] = []string{selected}
	case "manifest":
		executor.row["Digest"] = "sha256:" + strings.Repeat("3", 64)
		executor.row["RepoDigests"] = []string{runtime.Image}
	case "platform":
		executor.row["Architecture"] = "riscv64"
	}
	session, err := localexec.NewSession(executor, uint32(os.Geteuid()), t.TempDir(), time.Second) //nolint:gosec // Linux geteuid returns the unsigned 32-bit syscall identity.
	if err != nil {
		t.Fatal(err)
	}
	observed, err := descriptorImage(context.Background(), podman.New(session), runtime)
	if attack != "none" {
		if !errors.Is(err, datapublish.ErrRecord) {
			t.Fatal("recorded identity drift admitted", attack, err)
		}
		return
	}
	if err != nil || observed.ImageID != runtime.ImageID || observed.Platform.Architecture != "arm64" || len(executor.commands) != 2 || executor.commands[0][2] != runtime.Image || executor.commands[1][2] != selected {
		t.Fatal("recorded pins or actual platform replaced by today's profile", err)
	}
}

func TestFreshNativeMaterialRefusesUnselectedImageBeforePull(t *testing.T) {
	f := freshNativeMaterialFixture(t)
	f.desired.SpacetimeDB.Image = spec.ImageReference("registry.example/unselected-engine@sha256:" + strings.Repeat("1", 64))
	if _, err := (nativeMaterialOperations{service: f.service}).candidate(context.Background(), f.plan, f.desired, nil, time.Now().UTC().Add(time.Hour)); err == nil {
		t.Fatal("unselected initial engine material admitted")
	}
}
