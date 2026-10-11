package restic

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/backupcredentials"
)

type S3Location struct {
	Endpoint  string
	Bucket    string
	Prefix    string
	Region    string
	PathStyle bool
}

func (l S3Location) SHA256() string {
	raw, _ := json.Marshal(struct {
		Endpoint, Bucket, Prefix, Region string
		PathStyle                        bool
	}{l.Endpoint, l.Bucket, l.Prefix, l.Region, l.PathStyle})
	return digest(raw)
}
func (S3Location) MarshalJSON() ([]byte, error) { return []byte(`{"private":true}`), nil }
func (S3Location) String() string               { return "[private backup location]" }
func (S3Location) GoString() string             { return "[private backup location]" }

var bucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
var regionPattern = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

// Repository keeps endpoint/password/credentials in a private closure. Scope
// must come from protected ownership, epoch and credential admission under the
// caller's locks. This transport constructor does not supply that authority.
type Repository struct {
	scope              Scope
	private            func() []string
	initializationPlan string
	pathStyle          bool
}

func (Repository) String() string               { return "[private encrypted repository]" }
func (Repository) GoString() string             { return "[private encrypted repository]" }
func (Repository) MarshalJSON() ([]byte, error) { return []byte(`{"private":true}`), nil }
func (Repository) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "[private encrypted repository]")
}

// NewS3Repository accepts an already admitted scope and delivered recovery
// material. A nonempty initializationPlan binds the separately reviewed grant
// for a fresh owned repository. Existing repositories use an empty plan.
func NewS3Repository(scope Scope, location S3Location, password []byte, packet backupcredentials.Packet, initializationPlan string) (Repository, error) {
	endpoint, err := url.Parse(location.Endpoint)
	if err != nil || endpoint.Scheme != "https" || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.Path != "" && endpoint.Path != "/" {
		return Repository{}, ErrInvalid
	}
	if !scope.valid() || scope.DestinationSHA256 != location.SHA256() || !bucketPattern.MatchString(location.Bucket) || !regionPattern.MatchString(location.Region) || !validPath(location.Prefix) {
		return Repository{}, ErrInvalid
	}
	if initializationPlan != "" && !hashPattern.MatchString(initializationPlan) {
		return Repository{}, ErrInvalid
	}
	environment := packet.Environment()
	if len(environment) < 2 || len(password) < 16 || len(password) > 4096 || strings.ContainsRune(string(password), 0) {
		return Repository{}, ErrInvalid
	}
	if expiry := packet.Expiry(); expiry != nil && scope.ExpiresAt.After(*expiry) {
		return Repository{}, ErrExpired
	}
	locationValue := "s3:" + strings.TrimSuffix(location.Endpoint, "/") + "/" + location.Bucket + "/" + location.Prefix
	private := append([]string{"RESTIC_REPOSITORY=" + locationValue, "RESTIC_PASSWORD=" + string(password), "AWS_DEFAULT_REGION=" + location.Region}, environment...)
	return Repository{scope: scope, private: func() []string { return append([]string{}, private...) }, initializationPlan: initializationPlan, pathStyle: location.PathStyle}, nil
}

func (r Repository) admit(deadline time.Time, identity Identity) error {
	if !r.scope.valid() || r.private == nil || r.scope.BindingID != identity.BindingID || r.scope.EpochID != identity.EpochID {
		return ErrInvalid
	}
	if !time.Now().UTC().Before(r.scope.ExpiresAt) || !deadline.Before(r.scope.ExpiresAt) {
		return ErrExpired
	}
	return nil
}
