package cli

import "testing"

func TestCheckToolMissing(t *testing.T) {
	got := checkTool("deployctl_definitely_not_an_executable_123")
	if got.Available || got.Path != "" {
		t.Fatalf("unexpected tool lookup result: %+v", got)
	}
}
