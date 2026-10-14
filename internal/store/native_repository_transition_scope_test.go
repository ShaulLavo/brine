package store

import (
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/restic"
)

func TestNativeIncomingTransitionScopeMatchesInitializedCanonicalLocation(t *testing.T) {
	binding := data.ReplicaBindingID(strings.Repeat("a", 32))
	epoch := data.ReplicaEpochID(strings.Repeat("b", 32))
	archive := data.ArchiveBinding{BindingID: binding, EpochID: epoch, RemotePrefix: "owned/incoming/", Destination: data.Destination{Endpoint: "https://objects.example.invalid", Bucket: "test-bucket", Region: "auto", PathStyle: true}}
	intent := nativeRepositoryIntent{record: nativeRepositoryIntentRecord{BindingID: binding}, epochID: epoch, archive: archive}
	scope := restic.Scope{BindingID: string(binding), EpochID: string(epoch), DestinationSHA256: nativeArchiveLocation(archive).SHA256()}
	if !nativeIncomingTransitionScopeMatches(scope, intent) {
		t.Fatal("canonical initialized scope refused a transition")
	}
	crossed := scope
	crossed.DestinationSHA256 = restic.S3Location{Endpoint: archive.Destination.Endpoint, Bucket: archive.Destination.Bucket, Prefix: archive.RemotePrefix, Region: archive.Destination.Region, PathStyle: archive.Destination.PathStyle}.SHA256()
	if nativeIncomingTransitionScopeMatches(crossed, intent) {
		t.Fatal("noncanonical trailing slash substituted for initialized destination")
	}
	for _, change := range []func(*restic.Scope){
		func(s *restic.Scope) { s.BindingID = strings.Repeat("c", 32) },
		func(s *restic.Scope) { s.EpochID = strings.Repeat("c", 32) },
		func(s *restic.Scope) { s.RepositoryID = strings.Repeat("c", 64) },
		func(s *restic.Scope) { s.DestinationSHA256 = strings.Repeat("c", 64) },
	} {
		crossed = scope
		change(&crossed)
		if nativeIncomingTransitionScopeMatches(crossed, intent) {
			t.Fatal("crossed repository scope admitted a transition")
		}
	}
	for _, change := range []func(*data.ArchiveBinding){
		func(a *data.ArchiveBinding) { a.RemotePrefix = "other/incoming/" },
		func(a *data.ArchiveBinding) { a.Destination.Bucket = "other-bucket" },
		func(a *data.ArchiveBinding) { a.Destination.Endpoint = "https://other.example.invalid" },
		func(a *data.ArchiveBinding) { a.Destination.Region = "other-region" },
		func(a *data.ArchiveBinding) { a.Destination.PathStyle = false },
	} {
		crossedIntent := intent
		change(&crossedIntent.archive)
		if nativeIncomingTransitionScopeMatches(scope, crossedIntent) {
			t.Fatal("different destination substituted for initialized repository")
		}
	}
}
