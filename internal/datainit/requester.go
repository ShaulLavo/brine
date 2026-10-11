package datainit

import (
	"encoding/json"
	"regexp"
)

// Requester is a server-established identity, not an initialization request field.
// Its private representation prevents callers from inventing a request identity.
type Requester struct{ identity string }

var restrictedIdentity = regexp.MustCompile(`^deploy:[a-f0-9]{64}$`)

// ParseRequester accepts only the server-established restricted-channel identity.
func ParseRequester(identity string) (Requester, error) {
	if !restrictedIdentity.MatchString(identity) {
		return Requester{}, ErrRefused
	}
	return Requester{identity: identity}, nil
}

func (r Requester) String() string { return r.identity }
func (r Requester) Valid() bool    { return restrictedIdentity.MatchString(r.identity) }
func (r Requester) MarshalJSON() ([]byte, error) {
	if !r.Valid() {
		return nil, ErrRefused
	}
	return json.Marshal(r.String())
}

// UnmarshalJSON reconstructs identity from immutable plans or response evidence;
// request admission never accepts this field or an entire caller-supplied plan.
func (r *Requester) UnmarshalJSON(raw []byte) error {
	var identity string
	if json.Unmarshal(raw, &identity) != nil {
		return ErrRefused
	}
	parsed, err := ParseRequester(identity)
	if err != nil {
		return err
	}
	*r = parsed
	return nil
}
