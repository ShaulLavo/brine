package datapublish

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

type RecoveryReleaseReference struct {
	ReleaseID            string `json:"release_id"`
	CodeSHA256           string `json:"code_sha256"`
	CompatibilitySHA256  string `json:"compatibility_sha256"`
	AcceptedMarker       string `json:"accepted_marker"`
	AcceptedSchemaSHA256 string `json:"accepted_schema_sha256"`
}

// RecoveryReview describes expected release references. Protected review
// admission establishes their actual existence, compatibility and approval.
type RecoveryReview struct {
	kind     string
	releases []RecoveryReleaseReference
}

func NoPriorReleaseReview() RecoveryReview { return RecoveryReview{kind: "no_prior_release"} }

func SelectedRecoveryReleases(releases []RecoveryReleaseReference) (RecoveryReview, error) {
	if len(releases) == 0 || len(releases) > 16 || !slices.IsSortedFunc(releases, compareRecoveryRelease) {
		return RecoveryReview{}, ErrRecord
	}
	for i, release := range releases {
		if !operationReference.MatchString(release.ReleaseID) || !digest(release.CodeSHA256) || !digest(release.CompatibilitySHA256) || !data.ValidMarker(release.AcceptedMarker) || release.AcceptedMarker == data.EmptyMarker || !digest(release.AcceptedSchemaSHA256) || i > 0 && releases[i-1].ReleaseID == release.ReleaseID {
			return RecoveryReview{}, ErrRecord
		}
	}
	return RecoveryReview{kind: "selected_releases", releases: slices.Clone(releases)}, nil
}

func compareRecoveryRelease(first, second RecoveryReleaseReference) int {
	return strings.Compare(first.ReleaseID, second.ReleaseID)
}

func (r RecoveryReview) Kind() string                         { return r.kind }
func (r RecoveryReview) Releases() []RecoveryReleaseReference { return slices.Clone(r.releases) }
func (r RecoveryReview) clone() RecoveryReview {
	return RecoveryReview{kind: r.kind, releases: slices.Clone(r.releases)}
}

func (r RecoveryReview) valid(kind SourceKind, schemaSHA256 string) bool {
	if r.kind == "no_prior_release" {
		return kind == CreateSource && len(r.releases) == 0
	}
	if r.kind != "selected_releases" {
		return false
	}
	if _, err := SelectedRecoveryReleases(r.releases); err != nil {
		return false
	}
	for _, release := range r.releases {
		if release.AcceptedSchemaSHA256 != schemaSHA256 {
			return false
		}
	}
	return true
}

func (r RecoveryReview) MarshalJSON() ([]byte, error) {
	if r.kind == "no_prior_release" && len(r.releases) == 0 {
		return json.Marshal(struct {
			Kind string `json:"kind"`
		}{r.kind})
	}
	if r.kind != "selected_releases" {
		return nil, ErrRecord
	}
	if _, err := SelectedRecoveryReleases(r.releases); err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Kind     string                     `json:"kind"`
		Releases []RecoveryReleaseReference `json:"releases"`
	}{r.kind, r.releases})
}

func (r *RecoveryReview) UnmarshalJSON(raw []byte) error {
	var tag struct {
		Kind string `json:"kind"`
	}
	if json.Unmarshal(raw, &tag) != nil {
		return ErrRecord
	}
	if tag.Kind == "no_prior_release" {
		if _, err := strictjson.Object(raw, "kind"); err != nil {
			return ErrRecord
		}
		*r = NoPriorReleaseReview()
		return nil
	}
	fields, err := strictjson.Object(raw, "kind", "releases")
	if err != nil || tag.Kind != "selected_releases" {
		return ErrRecord
	}
	var rows []json.RawMessage
	if json.Unmarshal(fields["releases"], &rows) != nil || len(rows) == 0 || len(rows) > 16 {
		return ErrRecord
	}
	releases := make([]RecoveryReleaseReference, len(rows))
	for i, row := range rows {
		if _, err = strictjson.Object(row, "release_id", "code_sha256", "compatibility_sha256", "accepted_marker", "accepted_schema_sha256"); err != nil || json.Unmarshal(row, &releases[i]) != nil {
			return ErrRecord
		}
	}
	parsed, err := SelectedRecoveryReleases(releases)
	if err == nil {
		*r = parsed
	}
	return err
}
