package planview

import (
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/plan"
)

func TestDiffLinesSanitizes(t *testing.T) {
	d := &plan.ConfigurationDiff{Environment: &plan.EnvironmentChange{Changed: []string{"A\x1b[31m"}}}
	got := strings.Join(DiffLines(d), "\n")
	if strings.Contains(got, "\x1b") || !strings.Contains(got, "value hidden") {
		t.Fatalf("%q", got)
	}
}
