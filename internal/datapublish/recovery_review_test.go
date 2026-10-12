package datapublish

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestRecoveryReviewSeparatesTruthfulInitialAbsenceFromSelectedReleases(t *testing.T) {
	initial := planFixture(t, true)
	if initial.Review.Recovery.Kind() != "no_prior_release" || len(initial.Review.Recovery.Releases()) != 0 {
		t.Fatal("initial syntax invented an app recovery release")
	}
	if _, err := NewPlan(initial); err != nil {
		t.Fatal("explicit initial recovery absence refused", err)
	}
	update := planFixture(t, false)
	update.Review.Recovery = NoPriorReleaseReview()
	if _, err := NewPlan(update); !errors.Is(err, ErrRecord) {
		t.Fatal("initialized history claimed no prior release", err)
	}
	update = planFixture(t, false)
	releases := update.Review.Recovery.Releases()
	releases[0].AcceptedSchemaSHA256 = strings.Repeat("f", 64)
	update.Review.Recovery, _ = SelectedRecoveryReleases(releases)
	if _, err := NewPlan(update); !errors.Is(err, ErrRecord) {
		t.Fatal("release compatibility expected a foreign target schema", err)
	}
}

func TestRecoveryReviewRejectsMixedArmsUnknownFieldsAndDuplicateReferences(t *testing.T) {
	for _, raw := range []string{
		`{"kind":"no_prior_release","releases":[]}`,
		`{"kind":"no_prior_release","approved":true}`,
		`{"kind":"selected_releases","releases":null}`,
		`{"kind":"selected_releases","releases":[]}`,
		`{"kind":"selected_releases","releases":[null]}`,
		`{"kind":"anything"}`,
	} {
		var review RecoveryReview
		if err := json.Unmarshal([]byte(raw), &review); err == nil {
			t.Fatal("mixed/unknown recovery syntax admitted", raw)
		}
	}
	releases := planFixture(t, false).Review.Recovery.Releases()
	if _, err := SelectedRecoveryReleases(append(releases, releases...)); err == nil {
		t.Fatal("duplicate release references admitted")
	}
}
