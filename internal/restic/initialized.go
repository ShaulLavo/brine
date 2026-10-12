package restic

import (
	"errors"
	"time"
)

// InitializedRepository is minted only by authenticated fixed config readback
// under the original creation grant. A Repository expectation cannot create it.
type InitializedRepository struct {
	repository    Repository
	creationGrant string
	configSHA256  string
	verifiedAt    time.Time
}

func (r InitializedRepository) Repository() Repository      { return r.repository }
func (r InitializedRepository) Scope() Scope                { return r.repository.scope }
func (r InitializedRepository) CreationGrantSHA256() string { return r.creationGrant }
func (r InitializedRepository) ConfigurationSHA256() string { return r.configSHA256 }
func (r InitializedRepository) VerifiedAt() time.Time       { return r.verifiedAt }

func (InitializedRepository) MarshalJSON() ([]byte, error) {
	return nil, errors.New("repository initialization proof cannot be serialized")
}

func (r InitializedRepository) AdmitsCreation(scope Scope, grantSHA256 string) bool {
	expected := r.repository.scope
	expected.RepositoryID = ""
	return scope.RepositoryID == "" && expected == scope && hashPattern.MatchString(r.repository.scope.RepositoryID) && hashPattern.MatchString(r.configSHA256) && hashPattern.MatchString(grantSHA256) && r.creationGrant == grantSHA256 && r.repository.private != nil && !r.verifiedAt.IsZero() && r.verifiedAt.Location() == time.UTC && !r.verifiedAt.After(time.Now().UTC()) && r.verifiedAt.Before(scope.ExpiresAt)
}
