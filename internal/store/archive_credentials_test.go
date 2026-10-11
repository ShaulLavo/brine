//go:build linux

package store

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/backupcredentials"
)

func TestArchiveCredentialsPersistExactOriginalScopeWithoutActivation(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	_, p, d := storedMaintenancePlan(t, s)
	binding := p.Candidate.Databases[0].Epochs[0]
	scope := backupcredentials.ArchiveScope{ArchiveID: string(p.ArchiveID), Binding: string(binding.BindingID), Epoch: string(binding.EpochID), CredentialRef: binding.Destination.CredentialRef, Destination: string(binding.Destination.Reference), TargetHash: p.TargetHash, PolicyHash: p.PolicyHash, RetainUntil: d.RetainUntil}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil { // #nosec G302 -- Private fixture directory requires owner traversal.
		t.Fatal(err)
	}
	service := backupcredentials.ArchiveService{Requester: p.Requester, Journal: s, Files: backupcredentials.Files{Root: root}, Scope: func(context.Context, string, string, string) (backupcredentials.ArchiveScope, error) {
		return scope, nil
	}, Lock: func(context.Context) (func() error, error) { return func() error { return nil }, nil }}
	plan, err := service.Plan(ctx, scope.ArchiveID, scope.Binding, scope.Epoch, nil)
	if err != nil {
		t.Fatal(err)
	}
	packet, err := backupcredentials.ReadPacket(strings.NewReader(`{"access_key_id":"PLANTED_ACCESS","secret_access_key":"PLANTED_SECRET"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer packet.Clear()
	receipt, err := service.Deliver(ctx, plan.ID, packet)
	if err != nil {
		t.Fatal(err)
	}
	// A recreated app uses independent binding/epoch identities. Its existence
	// neither selects nor relabels the archived credential receipt.
	fresh, err := s.ReserveDatabase(ctx, dataRequest())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadArchiveCredentialReceipt(ctx, p.ArchiveID, fresh.Replica.BindingID, fresh.Replica.EpochID); err == nil {
		t.Fatal("recreated binding selected old archive credentials")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := reopened.Close(); err != nil {
			t.Error(err)
		}
	}()
	got, err := reopened.ReadArchiveCredentialReceipt(ctx, p.ArchiveID, binding.BindingID, binding.EpochID)
	if err != nil || !reflect.DeepEqual(got, receipt) {
		t.Fatal("archive scope lost across reopen", got, err)
	}
	permit, err := reopened.ReadReplicaPermit(ctx, p.Candidate.Databases[0].Binding.DatabaseID)
	if err != nil || permit.FenceState != "held" {
		t.Fatal("renewal changed held fence", permit, err)
	}
	wrong := scope
	wrong.Epoch = strings.Repeat("f", 32)
	service.Journal = reopened
	service.Scope = func(context.Context, string, string, string) (backupcredentials.ArchiveScope, error) {
		return wrong, nil
	}
	if _, err := service.Plan(ctx, wrong.ArchiveID, wrong.Binding, wrong.Epoch, nil); !errors.Is(err, backupcredentials.ErrStorage) {
		t.Fatal("unrecorded original epoch admitted", err)
	}
	before := got.Plan.Scope.RetainUntil
	if !before.Equal(d.CommittedAt.Add(30 * 24 * time.Hour)) {
		t.Fatal("renewal changed immutable retention clock")
	}
}
