package sqliteprep

import (
	"errors"
	"slices"
	"testing"
)

// liveAbandonFixture abandons a claimed fresh LTX predecessor that recorded no
// stopped baseline: its owned replica still runs and remote credentials have
// expired, so no barrier, verification or upload could succeed.
func liveAbandonFixture() (Service, *fakeJournal, *fakeHost, Task, Input) {
	s, j, h, task, in := supersedeFixture()
	l := j.facts.Lineage
	l.Baseline, l.Coverage, l.OwnStopped, l.OwnUploaded = nil, nil, false, false
	in = Input{Kind: AbandonTask, PreparationID: l.Previous.Input.PreparationID, App: in.App, DatabaseID: in.DatabaseID}
	task.Kind = AbandonTask
	task.InputSHA256 = mustHash(in)
	j.facts.CredentialExpiresAt = testNow
	h.obs.Replica = Running
	h.obs.WritersStopped = true
	return s, j, h, task, in
}

func callIndex(calls []string, name string) int {
	return slices.Index(calls, name)
}

func assertLocalOnly(t *testing.T, calls []string) {
	t.Helper()
	for _, effect := range []string{"barrier", "verify", "upload", "capture"} {
		if slices.Contains(calls, effect) {
			t.Fatal(effect, calls)
		}
	}
}

func TestServiceAbandonSettlesLiveFailureWithoutRemote(t *testing.T) {
	s, j, h, task, in := liveAbandonFixture()
	r, err := s.Abandon(t.Context(), task, in)
	if err != nil || r.Receipt.Outcome != AbandonedOutcome || r.Receipt.Fence.State != "released" || h.held {
		t.Fatal(r, err, h.calls)
	}
	claim, stop := callIndex(h.calls, "claim_abandon"), callIndex(h.calls, "stop_replica")
	if claim < 0 || stop < claim || slices.Contains(h.calls, "stop_writers") || j.stop == nil || !j.stop.StopReplica || j.stop.StopWriters {
		t.Fatal("stop without a prior durable intent", h.calls, j.stop)
	}
	assertLocalOnly(t, h.calls)
}

func TestServiceAbandonStopsWritersBeforeReplica(t *testing.T) {
	s, j, h, task, in := liveAbandonFixture()
	h.obs.WritersStopped = false
	if _, err := s.Abandon(t.Context(), task, in); err != nil {
		t.Fatal(err, h.calls)
	}
	claim, writers, replica := callIndex(h.calls, "claim_abandon"), callIndex(h.calls, "stop_writers"), callIndex(h.calls, "stop_replica")
	if claim < 0 || writers < claim || replica < writers || !j.stop.StopWriters || !j.stop.StopReplica {
		t.Fatal(h.calls, j.stop)
	}
	assertLocalOnly(t, h.calls)
}

func TestServiceAbandonUnknownStopIsRecoveryRequired(t *testing.T) {
	for _, effect := range []string{"stop_writers", "stop_replica"} {
		t.Run(effect, func(t *testing.T) { checkAbandonUnknownStop(t, effect) })
	}
}

func checkAbandonUnknownStop(t *testing.T, effect string) {
	s, j, h, task, in := liveAbandonFixture()
	h.obs.WritersStopped = false
	h.fail = effect
	_, err := s.Abandon(t.Context(), task, in)
	if !errors.Is(err, ErrRecovery) || j.status.State != RecoveryRequired || j.completeCalls != 0 || h.held {
		t.Fatal(err, j.status, h.calls)
	}
	calls := len(h.calls)
	h.fail = ""
	if _, err = s.Abandon(t.Context(), task, in); !errors.Is(err, ErrRecovery) || len(h.calls) != calls {
		t.Fatal("same key repeated effects", err, h.calls)
	}
}

func TestServiceAbandonStopClaimFaults(t *testing.T) {
	for fault, want := range map[string]TaskState{"abandon_before": Failed, "abandon_after": RecoveryRequired} {
		t.Run(fault, func(t *testing.T) { checkAbandonStopClaimFault(t, fault, want) })
	}
}

func checkAbandonStopClaimFault(t *testing.T, fault string, want TaskState) {
	s, j, h, task, in := liveAbandonFixture()
	j.fail = fault
	if _, err := s.Abandon(t.Context(), task, in); err == nil || j.status.State != want || slices.Contains(h.calls, "stop_replica") || slices.Contains(h.calls, "stop_writers") {
		t.Fatal(err, j.status, h.calls)
	}
}

func TestServiceAbandonRefusesStopsWithoutClaim(t *testing.T) {
	for name, edit := range map[string]func(*fakeJournal, *fakeHost){
		"baseline_then_running": func(j *fakeJournal, _ *fakeHost) {
			l := fixtureLineage()
			j.facts.Lineage = &l
		},
		"baseline_then_writers": func(j *fakeJournal, h *fakeHost) {
			l := fixtureLineage()
			j.facts.Lineage = &l
			h.obs.Replica = Stopped
			h.obs.WritersStopped = false
		},
		"unowned":        func(_ *fakeJournal, h *fakeHost) { h.obs.Owned = false },
		"jobs":           func(_ *fakeJournal, h *fakeHost) { h.obs.JobsAbsent = false },
		"unknown_state":  func(_ *fakeJournal, h *fakeHost) { h.obs.Replica = "activating" },
		"foreign_schema": func(_ *fakeJournal, h *fakeHost) { h.obs.Schema.Marker = "v9" },
	} {
		t.Run(name, func(t *testing.T) { checkAbandonRefusesStop(t, edit) })
	}
}

func checkAbandonRefusesStop(t *testing.T, edit func(*fakeJournal, *fakeHost)) {
	s, j, h, task, in := liveAbandonFixture()
	edit(j, h)
	_, err := s.Abandon(t.Context(), task, in)
	var refusal *Refusal
	if !errors.As(err, &refusal) || j.status.State != Failed || j.stop != nil || slices.Contains(h.calls, "stop_replica") || slices.Contains(h.calls, "stop_writers") {
		t.Fatal(err, j.status, h.calls)
	}
}

func TestAbandonStopRecordIsClosed(t *testing.T) {
	_, j, _, task, in := liveAbandonFixture()
	valid := AbandonStop{Task: task, Input: in, IntentSHA256: mustHash(j.facts.Lineage.Previous), Fence: j.facts.Fence, PolicySHA256: hash("a"), StopReplica: true, ClaimedAt: testNow}
	if _, err := Encode(valid); err != nil {
		t.Fatal(err)
	}
	for name, edit := range map[string]func(*AbandonStop){
		"no_stop":       func(s *AbandonStop) { s.StopReplica = false },
		"prepare_task":  func(s *AbandonStop) { s.Task.Kind = PrepareTask },
		"crossed_input": func(s *AbandonStop) { s.Input.PreparationID = id("7") },
		"released":      func(s *AbandonStop) { s.Fence.State = "released" },
		"other_db":      func(s *AbandonStop) { s.Fence.DatabaseID = "other-database" },
		"intent_hash":   func(s *AbandonStop) { s.IntentSHA256 = "short" },
		"local_time":    func(s *AbandonStop) { s.ClaimedAt = testNow.Local() },
	} {
		s := valid
		edit(&s)
		if s.Validate() == nil {
			t.Fatal(name)
		}
	}
	raw, _ := Encode(valid)
	if decoded, err := DecodeAbandonStop(raw); err != nil || decoded != valid {
		t.Fatal(err)
	}
	if _, err := DecodeAbandonStop(append(raw[:len(raw):len(raw)], ' ')); err == nil {
		t.Fatal("noncanonical stop decoded")
	}
}
