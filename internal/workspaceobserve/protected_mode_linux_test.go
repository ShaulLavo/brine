//go:build linux

package workspaceobserve

import (
	"os"
	"testing"
)

func TestProtectedReadMode(t *testing.T) {
	for _, mode := range []os.FileMode{0600, 0640, 0644} {
		if !protectedReadMode(mode) {
			t.Errorf("root-owned read-only policy mode %04o refused", mode)
		}
	}
	for _, mode := range []os.FileMode{0400, 0604, 0620, 0660, 0664, 0666, 0700, 0744} {
		if protectedReadMode(mode) {
			t.Errorf("unapproved protected file mode %04o admitted", mode)
		}
	}
}
