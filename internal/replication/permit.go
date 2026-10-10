package replication

import (
	"context"
	"errors"
)

var ErrPermit = errors.New("replication: startup permit refused")

type FenceState string

const (
	Unfenced      FenceState = "unfenced"
	FenceReleased FenceState = "released"
	FenceHeld     FenceState = "held"
)

type DestinationOwnership string

const (
	LocalOwner   DestinationOwnership = "local"
	ForeignOwner DestinationOwnership = "foreign"
)

// PermitState is a fresh, read-only committed-state projection. Missing records
// are errors, not an unfenced default. SourceSettled requires verified bundle,
// epoch and destination reservation consistency. WriterCompatible additionally
// requires a fresh D12 schema observation against the committed release.
type PermitState struct {
	Binding          Binding
	Config           []byte
	ConfigHash       string
	ConfigPath       string
	CredentialPath   string
	LifetimeLock     string
	Fence            FenceState
	Ownership        DestinationOwnership
	SourceSettled    bool
	WriterCompatible bool
}
type PermitReader interface {
	ReadReplicaPermit(context.Context, string) (PermitState, error)
	ReadWriterPermits(context.Context, string) ([]PermitState, error)
}
type ReplicaPermitRequest struct{ DatabaseID, BindingID, EpochID, ConfigHash string }

func ReplicaPermit(ctx context.Context, r PermitReader, req ReplicaPermitRequest) error {
	if r == nil || ctx.Err() != nil || !idPattern.MatchString(req.DatabaseID) || !idPattern.MatchString(req.BindingID) || !idPattern.MatchString(req.EpochID) || !hashPattern.MatchString(req.ConfigHash) {
		return ErrPermit
	}
	s, err := r.ReadReplicaPermit(ctx, req.BindingID)
	if err != nil || ctx.Err() != nil || !matchesRequest(s, req) {
		return ErrPermit
	}
	return nil
}
func WriterPermit(ctx context.Context, r PermitReader, incarnationID string) error {
	if r == nil || ctx.Err() != nil || !idPattern.MatchString(incarnationID) {
		return ErrPermit
	}
	states, err := r.ReadWriterPermits(ctx, incarnationID)
	if err != nil || ctx.Err() != nil || len(states) == 0 {
		return ErrPermit
	}
	seen := map[string]bool{}
	bindings := map[string]bool{}
	for _, s := range states {
		if !validPermit(s) || s.Binding.IncarnationID != incarnationID || !s.WriterCompatible || seen[s.Binding.DatabaseID] || bindings[s.Binding.BindingID] {
			return ErrPermit
		}
		seen[s.Binding.DatabaseID] = true
		bindings[s.Binding.BindingID] = true
	}
	return nil
}
func validPermit(s PermitState) bool {
	return (s.Fence == Unfenced || s.Fence == FenceReleased) && validBinding(s)
}
func validBinding(s PermitState) bool {
	if s.Ownership != LocalOwner || !s.SourceSettled || !hashPattern.MatchString(s.ConfigHash) || ConfigHash(s.Config) != s.ConfigHash {
		return false
	}
	_, err := ParseConfig(s.Config, s.Binding)
	return err == nil
}

func matchesRequest(s PermitState, req ReplicaPermitRequest) bool {
	return validPermit(s) && matchesIdentity(s, req)
}
func matchesIdentity(s PermitState, req ReplicaPermitRequest) bool {
	return s.Binding.DatabaseID == req.DatabaseID && s.Binding.BindingID == req.BindingID && s.Binding.EpochID == req.EpochID && s.ConfigHash == req.ConfigHash
}
