// Package admission implements dormant local admission file and lease operations.
// It grants no host authority. Root callers must hold the enrollment record lock,
// verify host identity and exact record/pending consistency, and journal every
// effect and terminal reopening obligation outside this package.
package admission

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"time"
)

type Kind string
type Stage string
type Quiescence string

const (
	Update         Kind       = "update"
	ControlRestore Kind       = "control-restore"
	Withdrawal     Kind       = "withdrawal"
	Maintenance    Kind       = "maintenance"
	Admitting      Stage      = "admitting"
	Fenced         Stage      = "fenced"
	Captured       Stage      = "captured"
	Replacing      Stage      = "replacing"
	Activating     Stage      = "activating"
	Migrating      Stage      = "migrating"
	Quarantining   Stage      = "quarantining"
	Installing     Stage      = "installing"
	Opening        Stage      = "opening"
	Issuing        Stage      = "issuing"
	Settling       Stage      = "settling"
	Withdrawing    Stage      = "withdrawing"
	Terminal       Stage      = "terminal"
	Store          Quiescence = "store"
	Runner         Quiescence = "runner"
)

const MaxStateBytes = 16 << 10

var (
	ErrUnavailable  = errors.New("admission_unavailable")
	ErrClosed       = errors.New("admission_closed")
	ErrInconsistent = errors.New("admission_inconsistent")
	ErrDrainTimeout = errors.New("admission_drain_timeout")
	tokenPattern    = regexp.MustCompile(`^[a-f0-9]{32}$`)
)

type State struct {
	Version int      `json:"version"`
	Open    bool     `json:"open"`
	Pending *Pending `json:"pending,omitempty"`
}

type Pending struct {
	Kind          Kind       `json:"kind"`
	ID            string     `json:"id"`
	Stage         Stage      `json:"stage"`
	Quiescence    Quiescence `json:"quiescence"`
	ExemptOpen    bool       `json:"exempt_open"`
	ExemptAttempt string     `json:"exempt_attempt,omitempty"`
	Since         string     `json:"since"`
}

// Exemption selects an exact root-published attempt. Possessing one is not
// authority: AcquireExempt also needs a protected enabled state and stage lease.
type Exemption struct {
	Kind    Kind
	ID      string
	Stage   Stage
	Attempt string
}

func Decode(raw []byte) (State, error) {
	if len(raw) > MaxStateBytes {
		return State{}, ErrUnavailable
	}
	fields, err := strictObject(raw, []string{"version", "open"}, []string{"pending"})
	if err != nil {
		return State{}, fmt.Errorf("%w: state: %w", ErrUnavailable, err)
	}
	if pending, ok := fields["pending"]; ok {
		_, err = strictObject(pending, []string{"kind", "id", "stage", "quiescence", "exempt_open", "since"}, []string{"exempt_attempt"})
		if err != nil {
			return State{}, fmt.Errorf("%w: pending: %w", ErrUnavailable, err)
		}
	}
	var s State
	if err = json.Unmarshal(raw, &s); err != nil {
		return State{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if err = validate(s); err != nil {
		return State{}, err
	}
	return s, nil
}

func Encode(s State) ([]byte, error) {
	if err := validate(s); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return raw, nil
}

func validate(s State) error {
	if s.Version != 1 || s.Open != (s.Pending == nil) {
		return ErrUnavailable
	}
	if s.Pending == nil {
		return nil
	}
	p := s.Pending
	if !validKind(p.Kind) || !validStage(p.Stage) || !tokenPattern.MatchString(p.ID) {
		return ErrUnavailable
	}
	if p.Quiescence != Store && p.Quiescence != Runner {
		return ErrUnavailable
	}
	if _, err := time.Parse(time.RFC3339, p.Since); err != nil {
		return ErrUnavailable
	}
	if p.ExemptAttempt != "" && !tokenPattern.MatchString(p.ExemptAttempt) {
		return ErrUnavailable
	}
	if p.ExemptOpen && (!tokenPattern.MatchString(p.ExemptAttempt) || !exemptStage(p.Kind, p.Stage)) {
		return ErrUnavailable
	}
	return nil
}

func validKind(k Kind) bool {
	return k == Update || k == ControlRestore || k == Withdrawal || k == Maintenance
}

func validStage(s Stage) bool {
	switch s {
	case Admitting, Fenced, Captured, Replacing, Activating, Migrating, Quarantining, Installing, Opening, Issuing, Settling, Withdrawing, Terminal:
		return true
	default:
		return false
	}
}

func exemptStage(k Kind, s Stage) bool {
	return k == Update && s == Migrating || k == ControlRestore && s == Opening
}

func strictObject(raw []byte, required, optional []string) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("expected object")
	}
	fields := make(map[string]json.RawMessage)
	for d.More() {
		if err = readField(d, fields, required, optional); err != nil {
			return nil, err
		}
	}
	if _, err = d.Token(); err != nil {
		return nil, err
	}
	if _, err = d.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing JSON")
	}
	for _, key := range required {
		if _, ok := fields[key]; !ok {
			return nil, fmt.Errorf("missing %s", key)
		}
	}
	return fields, nil
}

func readField(d *json.Decoder, fields map[string]json.RawMessage, required, optional []string) error {
	token, err := d.Token()
	if err != nil {
		return err
	}
	key, ok := token.(string)
	if !ok || !contains(required, key) && !contains(optional, key) {
		return errors.New("unknown field")
	}
	if _, ok = fields[key]; ok {
		return errors.New("duplicate field")
	}
	var raw json.RawMessage
	if err = d.Decode(&raw); err != nil {
		return err
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("null %s", key)
	}
	fields[key] = raw
	return nil
}

func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
