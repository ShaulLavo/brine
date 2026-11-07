//go:build linux

package host

import (
	"os"
	"slices"
	"strings"
	"testing"
)

func TestAdmissionFixtureNamespaceMaps(t *testing.T) {
	prefix, err := fixtureNamespacePrefix()
	if err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		if !slices.Contains(prefix, "--map-root-user") {
			t.Fatal("root fixture lost its root mapping", prefix)
		}
		return
	}
	if !slices.Contains(prefix, "--map-current-user") {
		t.Error("original test user must have its own explicit mapping", prefix)
	}
	joined := strings.Join(prefix, "\x00")
	if strings.Count(joined, "--map-users=") != 1 || strings.Count(joined, "--map-groups=") != 1 {
		t.Fatal("namespace root must use one subordinate range per identity class", prefix)
	}
}
