package replication

import (
	"context"
	"errors"
	"testing"
)

type permitReader struct {
	state PermitState
	err   error
	reads int
}

func (r *permitReader) ReadReplicaPermit(context.Context, string) (PermitState, error) {
	r.reads++
	return r.state, r.err
}
func (r *permitReader) ReadWriterPermits(context.Context, string) ([]PermitState, error) {
	r.reads++
	if r.err != nil {
		return nil, r.err
	}
	return []PermitState{r.state}, nil
}
func allowedState(t *testing.T) PermitState {
	t.Helper()
	b := testBinding()
	raw, err := RenderConfig(b)
	if err != nil {
		t.Fatal(err)
	}
	return PermitState{Binding: b, Config: raw, ConfigHash: ConfigHash(raw), Fence: Unfenced, Ownership: LocalOwner, SourceSettled: true, WriterCompatible: true}
}
func TestReplicaPermitEveryStartFailsClosed(t *testing.T) {
	for _, start := range []string{"boot", "restart", "credential-rotation"} {
		t.Run(start, func(t *testing.T) {
			state := allowedState(t)
			r := &permitReader{state: state}
			req := ReplicaPermitRequest{DatabaseID: state.Binding.DatabaseID, BindingID: state.Binding.BindingID, EpochID: state.Binding.EpochID, ConfigHash: state.ConfigHash}
			if err := ReplicaPermit(context.Background(), r, req); err != nil {
				t.Fatal(err)
			}
			r.state.Fence = FenceHeld
			if err := ReplicaPermit(context.Background(), r, req); err == nil {
				t.Fatal("fenced restart permitted")
			}
			if r.reads != 2 {
				t.Fatal("permit was cached")
			}
		})
	}
}
func TestPermitsRejectUnknownAndInconsistentState(t *testing.T) {
	cases := map[string]func(*permitReader){"missing": func(r *permitReader) { r.err = errors.New("missing") }, "unreadable": func(r *permitReader) { r.err = errors.New("unreadable") }, "held": func(r *permitReader) { r.state.Fence = FenceHeld }, "unknown-fence": func(r *permitReader) { r.state.Fence = "" }, "foreign": func(r *permitReader) { r.state.Ownership = ForeignOwner }, "ambiguous-owner": func(r *permitReader) { r.state.Ownership = "" }, "unsettled": func(r *permitReader) { r.state.SourceSettled = false }, "config-hash": func(r *permitReader) { r.state.ConfigHash = "" }, "config-bytes": func(r *permitReader) { r.state.Config = []byte("unknown: true") }, "epoch": func(r *permitReader) { r.state.Binding.EpochID = "" }}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := allowedState(t)
			r := &permitReader{state: s}
			req := ReplicaPermitRequest{DatabaseID: s.Binding.DatabaseID, BindingID: s.Binding.BindingID, EpochID: s.Binding.EpochID, ConfigHash: s.ConfigHash}
			mutate(r)
			if ReplicaPermit(context.Background(), r, req) == nil {
				t.Fatal("unsafe replica permitted")
			}
			if WriterPermit(context.Background(), r, s.Binding.IncarnationID) == nil {
				t.Fatal("unsafe writer permitted")
			}
		})
	}
}
func TestWriterPermitRequiresFreshCompatibility(t *testing.T) {
	s := allowedState(t)
	r := &permitReader{state: s}
	if err := WriterPermit(context.Background(), r, s.Binding.IncarnationID); err != nil {
		t.Fatal(err)
	}
	r.state.WriterCompatible = false
	if WriterPermit(context.Background(), r, s.Binding.IncarnationID) == nil {
		t.Fatal("incompatible writer permitted")
	}
}
