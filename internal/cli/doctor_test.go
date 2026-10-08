package cli

import (
	"errors"
	"testing"
)

func TestCheckToolMissing(t *testing.T) {
	got := checkTool("missing-tool", func(name string) (string, error) {
		if name != "missing-tool" {
			t.Fatalf("lookup name = %q", name)
		}
		return "", errors.New("not found")
	})
	if got.Name != "missing-tool" || got.Available || got.Path != "" {
		t.Fatalf("unexpected tool lookup result: %+v", got)
	}
}
