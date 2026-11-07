package cli

import (
	"fmt"
	"strings"
)

func deploymentSummaryLines(s deploymentReviewSummary) []string {
	return []string{
		"Image " + s.Image,
		fmt.Sprintf("Container port %d", s.ContainerPort),
		"Domains " + strings.Join(s.Domains, ", "),
		fmt.Sprintf("Health %s, expected %d, startup %ds, timeout %ds", s.Health.Path, s.Health.ExpectedStatus, s.Health.StartupDeadlineSeconds, s.Health.TimeoutSeconds),
		"Environment keys " + strings.Join(s.EnvironmentKeys, ", ") + " (values hidden)",
	}
}
