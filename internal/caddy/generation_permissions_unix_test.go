//go:build unix

package caddy

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

func TestGenerationReadableWithPrivateJobUmask(t *testing.T) {
	if os.Getenv("BRINE_TEST_PRIVATE_UMASK") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestGenerationReadableWithPrivateJobUmask$")
		cmd.Env = append(os.Environ(), "BRINE_TEST_PRIVATE_UMASK=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("private job umask: %v\n%s", err, out)
		}
		return
	}
	syscall.Umask(0077)
	m, root, main, state, _, reload := setup(t)
	reload.observe = func() {
		for _, entry := range []struct {
			path string
			mode os.FileMode
		}{
			{"gen-1", 0755},
			{"gen-1/hello.caddy", 0644},
			{"candidate-gen-1.caddy", 0600},
		} {
			info, err := os.Stat(filepath.Join(root, entry.path))
			if err != nil || info.Mode().Perm() != entry.mode {
				t.Fatalf("%s must have mode %o before reload: %v, %v", entry.path, entry.mode, info, err)
			}
		}
	}
	if _, err := m.Apply(context.Background(), main, state, Put(fixtureSite(t))); err != nil {
		t.Fatal(err)
	}
	if reload.calls != 1 {
		t.Fatalf("reload count %d", reload.calls)
	}
}
