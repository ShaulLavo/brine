// Package datapublish models private publication review and retained cursors.
// Its records are expectations and grant no host or data mutation authority.
package datapublish

import (
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
)

var ErrRecord = errors.New("invalid publication record")
var ErrOperatorProcedure = errors.New("publication requires a separate operator procedure")
var ErrStale = errors.New("publication review is stale")
var ErrInspection = errors.New("publication requires retained-attempt inspection")

const RecordVersion = 1
const RecordLimit = 64 << 10
const MaxReviewLifetime = 15 * time.Minute

var appName = regexp.MustCompile(`^[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
var operationReference = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

func digest(value string) bool   { return data.ValidCatalogHash(value) }
func identity(value string) bool { return data.ValidID(value) }
func planID(value string) bool {
	return strings.HasPrefix(value, "sha256:") && digest(strings.TrimPrefix(value, "sha256:"))
}
func utc(value time.Time) bool { return !value.IsZero() && value.Location() == time.UTC }

type SourceKind string

const (
	CreateSource   SourceKind = "create"
	CapturedSource SourceKind = "captured"
)

type CoverageContract string

const ConfirmedRecoveryPairsV1 CoverageContract = "brine-recovery-pairs-confirmed-v1"

type SecretReferences struct {
	Publisher        data.EngineSecretVersion `json:"publisher"`
	SigningPublic    data.EngineSecretVersion `json:"signing_public"`
	SigningPrivate   data.EngineSecretVersion `json:"signing_private"`
	AuthPolicy       data.EngineSecretVersion `json:"auth_policy"`
	RecoveryPassword data.EngineSecretVersion `json:"recovery_password"`
}

func (s SecretReferences) valid() bool {
	refs := []data.EngineSecretVersion{s.Publisher, s.SigningPublic, s.SigningPrivate, s.AuthPolicy, s.RecoveryPassword}
	seen := make(map[string]bool, len(refs))
	for _, ref := range refs {
		if ref.Validate() != nil || seen[ref.Reference] {
			return false
		}
		seen[ref.Reference] = true
	}
	return true
}

type BindingFacts struct {
	App            string           `json:"app"`
	IncarnationID  string           `json:"incarnation_id"`
	DatabaseID     string           `json:"database_id"`
	BindingID      string           `json:"binding_id"`
	EpochID        string           `json:"epoch_id"`
	Generation     uint64           `json:"generation"`
	TargetSHA256   string           `json:"target_sha256"`
	PolicySHA256   string           `json:"policy_sha256"`
	DesiredSHA256  string           `json:"desired_sha256"`
	ConfigSHA256   string           `json:"config_sha256"`
	SecretVersions SecretReferences `json:"secret_versions"`
}

func (f BindingFacts) valid() bool {
	return appName.MatchString(f.App) && identity(f.IncarnationID) && identity(f.DatabaseID) && identity(f.BindingID) && identity(f.EpochID) &&
		digest(f.TargetSHA256) && digest(f.PolicySHA256) && digest(f.DesiredSHA256) && digest(f.ConfigSHA256) && f.SecretVersions.valid()
}
