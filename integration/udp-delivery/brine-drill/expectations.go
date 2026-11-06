package brinedrill

import (
	"errors"
	"slices"
)

// ReleaseExpectation pins the fixture identity independently of observed status.
// The Brine release ID is the successful apply's ledger-owned operation ID.
type ReleaseExpectation struct {
	Release     string `json:"release"`
	ImageDigest string `json:"image_digest"`
}

func (m Manifest) releaseExpectations() (map[string]ReleaseExpectation, error) {
	declarations := map[string]Step{}
	expectations := map[string]ReleaseExpectation{}
	for _, step := range m.Phases.allSteps() {
		if err := m.addReleaseExpectation(step, declarations, expectations); err != nil {
			return nil, err
		}
		declarations[step.ID] = step
	}
	return expectations, nil
}

func (m Manifest) addReleaseExpectation(step Step, prior map[string]Step, expectations map[string]ReleaseExpectation) error {
	if step.Expectation != nil {
		e := *step.Expectation
		if step.Action != ActionApply || step.App == "" || !releasePattern.MatchString(e.Release) || !digestPattern.MatchString(e.ImageDigest) || m.Fixture.ReleaseDigests[e.Release] != e.ImageDigest {
			return errors.New("step release expectation is not pinned")
		}
		if step.ExpectedOutcome == "" || step.ExpectedOutcome == "succeeded" {
			expectations[step.ID] = e
		}
	}
	needsRelease := step.Action == ActionRollback || step.Action == ActionProbe || step.Action == ActionStatus && slices.Contains([]string{"ready", "absent"}, step.ExpectedOutcome)
	if !needsRelease && step.ReleaseStep == "" {
		return nil
	}
	source, ok := prior[step.ReleaseStep]
	expected, declared := expectations[step.ReleaseStep]
	if !needsRelease || !ok || !declared || source.App != step.App || source.Action != ActionApply && source.Action != ActionRollback {
		return errors.New("release_step must name an earlier successful release step for the same app")
	}
	if step.Action == ActionRollback {
		expectations[step.ID] = expected
	}
	return nil
}

func (r *Runner) releaseFor(ledger Ledger, step Step) (ReleaseExpectation, string, error) {
	if ledger.Schema != LedgerSchema || ledger.RunID != r.Manifest.RunID || ledger.ManifestHash != manifestHash(r.Manifest) {
		return ReleaseExpectation{}, "", errors.New("release ledger does not match manifest")
	}
	expectations, err := r.Manifest.releaseExpectations()
	if err != nil {
		return ReleaseExpectation{}, "", err
	}
	expected, ok := expectations[step.ReleaseStep]
	if !ok {
		return ReleaseExpectation{}, "", errors.New("release expectation is missing")
	}
	source := r.Manifest.releaseStep(step.ReleaseStep)
	for _, entry := range ledger.Entries {
		if entry.ID != step.ReleaseStep {
			continue
		}
		if entry.App != step.App || entry.Action != source.Action || entry.State != "succeeded" || entry.OperationID == "" || entry.Key != operationKey(r.Manifest.RunID, source.ID) || entry.PlanID == "" || entry.CommandSHA256 != commandHash(r.applyCommand(entry.PlanID, entry.Key)) {
			return ReleaseExpectation{}, "", errors.New("release operation is not a settled ledger-owned apply")
		}
		if err := r.checkReleasePlan(ledger, source, entry); err != nil {
			return ReleaseExpectation{}, "", err
		}
		return expected, entry.OperationID, nil
	}
	return ReleaseExpectation{}, "", errors.New("release operation is missing from the ledger")
}

func (m Manifest) releaseStep(id string) Step {
	for _, step := range m.Phases.allSteps() {
		if step.ID == id {
			return step
		}
	}
	return Step{}
}

func (r *Runner) probeCommand(step Step, endpoint, release string) CommandSpec {
	return CommandSpec{Binary: r.Manifest.Source.ClientBinary.Path, Args: []string{"-endpoint", endpoint, "-nonce", step.Nonce, "-release", release, "-attempts", "3", "-attempt-timeout", "1s", "-overall-timeout", "5s", "-vantage", step.Vantage}}
}

func (p PhaseSteps) allSteps() []Step {
	return slices.Concat(p.BeforeReboot, p.AfterReboot, p.Cleanup, p.Inspect, p.Resume)
}

func (r *Runner) checkReleasePlan(ledger Ledger, source Step, entry LedgerEntry) error {
	if source.Action == ActionRollback {
		_, releaseID, err := r.releaseFor(ledger, source)
		if err != nil || entry.ReleaseID != releaseID {
			return errors.New("rollback release does not match its source step")
		}
		return nil
	}
	planID, err := r.planReference(ledger, source)
	if err != nil || entry.PlanID != planID {
		return errors.New("release plan does not match its source step")
	}
	return nil
}
