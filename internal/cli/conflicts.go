package cli

import (
	"fmt"
	"io"

	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/planview"
)

func printConflicts(out io.Writer, conflicts []plan.Diagnostic) error {
	for _, conflict := range conflicts {
		if _, err := fmt.Fprintln(out, planview.ConflictText(conflict)); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(out, "Resolve the plan's conflicts and plan again before applying.")
	return err
}
