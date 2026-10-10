//go:build linux

package inventory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
)

type mappingRunner func(context.Context, string, ...string) (string, error)

func (f mappingRunner) Run(ctx context.Context, path string, args ...string) (string, error) {
	return f(ctx, path, args...)
}
func TestMappingProbeUsesOnlyPrivateDisposableData(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	evidence, err := data.ProbeRoot(context.Background(), root)
	if err != nil {
		t.Skipf("probe filesystem: %v", err)
	}
	identity := data.RuntimeIdentity{UID: 10001, GID: 10002}
	runner := mappingRunner(func(ctx context.Context, path string, args ...string) (string, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 30*time.Second {
			t.Fatal("probe lacks bounded deadline")
		}
		if path != "podman" {
			t.Fatal("non-typed container invocation")
		}
		for _, arg := range []string{"--rm", "--network=none", "--pull=never", "--userns=keep-id:uid=10001,gid=10002", "--user=10001:10002", "--umask=0077", MappingProbeImage} {
			if !slices.Contains(args, arg) {
				t.Fatal("missing probe argument", arg)
			}
		}
		var directory string
		for _, arg := range args {
			if strings.HasPrefix(arg, "--volume=") {
				directory = strings.TrimSuffix(strings.TrimPrefix(arg, "--volume="), ":/data:rw")
			}
		}
		if filepath.Dir(directory) != root || !strings.HasPrefix(filepath.Base(directory), ".brine-mapping-") {
			t.Fatal("live mount used", directory)
		}
		p := filepath.Join(directory, "probe")
		raw, err := os.ReadFile(p)
		if err != nil || string(raw) != "host" {
			t.Fatal("missing host write", err)
		}
		if err = os.WriteFile(p, []byte("container"), 0600); err != nil {
			t.Fatal(err)
		}
		return "mapped\n", nil
	})
	got, err := ProbeDataMapping(context.Background(), runner, evidence, identity)
	if err != nil || !got.Admits(identity, evidence) {
		t.Fatalf("mapping %+v %v", got, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("probe left artifacts", err)
	}
}
func TestMappingProbeRefusesFailureAndForeignModes(t *testing.T) {
	for _, mode := range []string{"command fails", "wrong output", "foreign modes"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			evidence, err := data.ProbeRoot(context.Background(), root)
			if err != nil {
				t.Skipf("probe filesystem: %v", err)
			}
			runner := mappingRunner(func(_ context.Context, _ string, args ...string) (string, error) {
				if mode == "command fails" {
					return "", errors.New("denied")
				}
				if mode == "wrong output" {
					return "other", nil
				}
				for _, arg := range args {
					if strings.HasPrefix(arg, "--volume=") {
						directory := strings.TrimSuffix(strings.TrimPrefix(arg, "--volume="), ":/data:rw")
						p := filepath.Join(directory, "probe")
						if err := os.WriteFile(p, []byte("container"), 0600); err != nil {
							t.Fatal(err)
						}
						if err := os.Chmod(p, 0644); err != nil {
							t.Fatal(err)
						}
					}
				}
				return "mapped", nil
			})
			got, err := ProbeDataMapping(context.Background(), runner, evidence, data.RuntimeIdentity{UID: 10001, GID: 10001})
			if err == nil || got.KeepID {
				t.Fatal("unsafe mapping admitted")
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 0 {
				t.Fatal("failed probe left artifacts", err)
			}
		})
	}
}
