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
	"github.com/ShaulLavo/brine/internal/localexec"
)

type mappingRunner func(context.Context, string, ...string) (string, error)

func (f mappingRunner) Run(ctx context.Context, path string, args ...string) (string, error) {
	return "", errors.New("combined mapping output is forbidden")
}
func (f mappingRunner) CaptureStdout(ctx context.Context, _ int, path string, args ...string) (localexec.Capture, error) {
	stdout, err := f(ctx, path, args...)
	return localexec.Capture{Stdout: stdout}, err
}
func TestMappingProbeUsesOnlyPrivateDisposableData(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil { //nolint:gosec // Private directory requires execute permission for traversal.
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
		raw, err := os.ReadFile(p) //nolint:gosec // Path is a private test fixture or verified private probe file.
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
			if err := os.Chmod(root, 0700); err != nil { //nolint:gosec // Private directory requires execute permission for traversal.
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
						if err := os.Chmod(p, 0644); err != nil { //nolint:gosec // Intentional unsafe-permission fixture proves fail-closed refusal.
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

type mappingScriptRunner struct{ mode string }

func (mappingScriptRunner) Run(context.Context, string, ...string) (string, error) {
	return "", errors.New("combined mapping output is forbidden")
}

func (r mappingScriptRunner) CaptureStdout(ctx context.Context, limit int, path string, args ...string) (localexec.Capture, error) {
	if path != "podman" {
		return localexec.Capture{}, errors.New("unexpected mapping executable")
	}
	for _, arg := range args {
		if !strings.HasPrefix(arg, "--volume=") {
			continue
		}
		directory := strings.TrimSuffix(strings.TrimPrefix(arg, "--volume="), ":/data:rw")
		return (localexec.ExecRunner{}).CaptureStdout(ctx, limit, "sh", "-c", mappingShellFixture, "mapping-fixture", filepath.Join(directory, "probe"), r.mode)
	}
	return localexec.Capture{}, errors.New("missing disposable mount")
}

const mappingShellFixture = `
test "$(cat "$1")" = host || exit 2
rm "$1" || exit 2
umask 077
printf container > "$1" || exit 2
printf 'podman cgroup warning\n' >&2
case "$2" in
  wrong-stdout) printf 'mapped\nextra\n'; exit 0 ;;
  stderr-marker) printf 'mapped\n' >&2; printf other; exit 0 ;;
  nonzero) printf 'mapped\n'; exit 7 ;;
  foreign-mode) chmod 0644 "$1" || exit 2 ;;
  wrong-content) printf other > "$1" || exit 2 ;;
  stdout-overflow) printf 'mapped\n'; printf '%4200s' ''; exit 0 ;;
  stderr-overflow) printf '%4200s' '' >&2 ;;
esac
printf 'mapped\n'
`

func TestMappingProbeSeparatesRealProcessDiagnostics(t *testing.T) {
	for _, mode := range []string{"warning", "stderr-overflow", "stdout-overflow", "wrong-stdout", "stderr-marker", "nonzero", "foreign-mode", "wrong-content"} {
		t.Run(mode, func(t *testing.T) { testMappingScript(t, mode) })
	}
}

func testMappingScript(t *testing.T, mode string) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil { //nolint:gosec // Private directory requires execute permission for traversal.
		t.Fatal(err)
	}
	rootProof, err := data.ProbeRoot(context.Background(), root)
	if err != nil {
		t.Skipf("probe filesystem: %v", err)
	}
	identity := data.RuntimeIdentity{UID: 10001, GID: 10001}
	proof, err := ProbeDataMapping(context.Background(), mappingScriptRunner{mode: mode}, rootProof, identity)
	wantSuccess := mode == "warning" || mode == "stderr-overflow"
	if wantSuccess && (err != nil || !proof.Admits(identity, rootProof)) {
		t.Fatalf("stderr warning refused successful mapping: %+v %v", proof, err)
	}
	if !wantSuccess && (err == nil || proof.KeepID) {
		t.Fatalf("invalid mapping admitted: %+v %v", proof, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("process probe left artifacts", err)
	}
}

type combinedMappingRunner struct{}

func (combinedMappingRunner) Run(context.Context, string, ...string) (string, error) {
	return "mapped", nil
}

func TestMappingProbeRequiresCaptureRunnerBeforeFileEffects(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil { //nolint:gosec // Private directory requires execute permission for traversal.
		t.Fatal(err)
	}
	rootProof, err := data.ProbeRoot(context.Background(), root)
	if err != nil {
		t.Skipf("probe filesystem: %v", err)
	}
	proof, err := ProbeDataMapping(context.Background(), combinedMappingRunner{}, rootProof, data.RuntimeIdentity{UID: 10001, GID: 10001})
	if !errors.Is(err, data.ErrInvalid) || proof.KeepID {
		t.Fatalf("combined runner admitted: %+v %v", proof, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("unsupported runner left artifacts", err)
	}
}
