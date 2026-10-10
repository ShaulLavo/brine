package datainit

import (
	"encoding/json"
	"regexp"
)

// Requester is a server-established identity, not an initialization request field.
// Its private representation keeps agent identities separate from local authority.
type Requester struct{ identity string }

var restrictedIdentity = regexp.MustCompile(`^deploy:[a-f0-9]{64}$`)

// AgentRequester refuses the reserved local operator identity, including aliases.
func AgentRequester(identity string) (Requester, error) {
	if !restrictedIdentity.MatchString(identity) {
		return Requester{}, ErrRefused
	}
	return Requester{identity: identity}, nil
}

// LocalOperatorRequester is selected only by local host-command composition.
func LocalOperatorRequester() Requester   { return Requester{identity: "local-operator"} }
func (r Requester) String() string        { return r.identity }
func (r Requester) IsLocalOperator() bool { return r == LocalOperatorRequester() }
func (r Requester) Valid() bool {
	return r.IsLocalOperator() || restrictedIdentity.MatchString(r.identity)
}
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
	if identity == LocalOperatorRequester().identity {
		*r = LocalOperatorRequester()
		return nil
	}
	parsed, err := AgentRequester(identity)
	if err != nil {
		return err
	}
	*r = parsed
	return nil
}
