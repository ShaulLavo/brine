package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/replication"
)

type hostPermitReader struct {
	state replication.PermitState
	reads int
	err   error
}

func (r *hostPermitReader) ReadReplicaPermit(context.Context, string) (replication.PermitState, error) {
	r.reads++
	return r.state, r.err
}
func (r *hostPermitReader) ReadWriterPermits(context.Context, string) ([]replication.PermitState, error) {
	r.reads++
	return []replication.PermitState{r.state}, r.err
}
func (r *hostPermitReader) ReadCredentialEnvironment(context.Context, string) ([]string, error) {
	return nil, replication.ErrPermit
}
func TestHostPermitCommandsFailClosedWithoutReadonlyReader(t *testing.T) {
	for _, args := range [][]string{{"host", "writer-permit", strings.Repeat("4", 32)}, {"host", "replica-permit", strings.Repeat("1", 32), strings.Repeat("2", 32), strings.Repeat("3", 32), "sha256:" + strings.Repeat("a", 64)}} {
		root := NewRootCommand(Dependencies{Context: context.Background(), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}})
		root.SetArgs(args)
		if root.Execute() == nil {
			t.Fatal("missing permit state allowed")
		}
		if HostRuntimeRequested(args) {
			t.Fatal("permit command requested mutating runtime")
		}
	}
}
func TestHostWriterPermitRefusesWithoutFreshSchemaCompatibility(t *testing.T) {
	database := strings.Repeat("1", 32)
	binding := strings.Repeat("2", 32)
	epoch := strings.Repeat("3", 32)
	incarnation := strings.Repeat("4", 32)
	b := replication.Binding{Cadence: replication.DefaultCadence(), DatabaseID: database, BindingID: binding, EpochID: epoch, IncarnationID: incarnation, DBPath: "/srv/data/apps/" + incarnation + "/databases/" + database + "/app.db", SocketPath: "/srv/state/replication/" + binding + "/control.sock", Endpoint: "https://objects.example.invalid", Bucket: "backup-bucket", Prefix: "base/apps/" + incarnation + "/databases/" + database + "/epochs/" + epoch + "/", Region: "auto"}
	raw, err := replication.RenderConfig(b)
	if err != nil {
		t.Fatal(err)
	}
	r := &hostPermitReader{state: replication.PermitState{Binding: b, Config: raw, ConfigHash: replication.ConfigHash(raw), Fence: replication.Unfenced, Ownership: replication.LocalOwner, SourceSettled: true}}
	deps := Dependencies{Context: context.Background(), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, HostPermits: r}
	root := NewRootCommand(deps)
	root.SetArgs([]string{"host", "writer-permit", incarnation})
	if err = root.Execute(); !errors.Is(err, replication.ErrPermit) {
		t.Fatal("missing observer permitted writer", err)
	}
	r.state.WriterCompatible = true
	root = NewRootCommand(deps)
	root.SetArgs([]string{"host", "writer-permit", incarnation})
	if err = root.Execute(); err != nil {
		t.Fatal(err)
	}
	r.state.Fence = replication.FenceHeld
	root = NewRootCommand(deps)
	root.SetArgs([]string{"host", "replica-permit", database, binding, epoch, replication.ConfigHash(raw)})
	if root.Execute() == nil {
		t.Fatal("fence permitted replica")
	}
}
