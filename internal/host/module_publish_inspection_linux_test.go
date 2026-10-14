//go:build linux

package host

import (
	"context"
	"errors"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/store"
)

func TestPublicationOriginalObservationCarriesVerifiedRecordedSeal(t *testing.T) {
	binding, _ := engineSourceFixture(t)
	root, err := openEngineSource(binding)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	ctx := context.Background()
	limits := engineCopyLimits{Bytes: 1 << 20, Files: 128, Depth: 16}
	sealed, err := sealOwnedEngineSource(ctx, root, limits)
	if err != nil {
		t.Fatal(err)
	}
	stable := engineSourceSealDigest(sealed)
	if stable == sealed.hash {
		t.Fatal("fixture does not distinguish recorded transition and generic hashes")
	}
	for _, recorded := range []string{stable, sealed.hash} {
		w := publicationPrivateInspectionWork{original: root, facts: store.PublicationPrivateInspectionFacts{
			Point:  store.NativePreparedPointView{SourceSealSHA256: recorded},
			Permit: store.EnginePermit{Runtime: data.EngineRuntime{CopyBytes: limits.Bytes, CopyFiles: limits.Files}},
		}}
		before, observed, err := w.observeOriginal(ctx)
		if err != nil || observed.OriginalSealSHA256 != recorded || observed.OriginalRoot != root || before.hash != sealed.hash {
			t.Fatal("verified source observation changed the immutable recorded digest", err)
		}
		if observed.WorkingRoot != nil || !observed.EngineAbsent || !observed.ApplicationAbsent || !observed.IngressClosed || !observed.PrivateReadersAbsent {
			t.Fatal("source observation lost its existing absence fields")
		}
	}
	if err := root.WriteFile("control-db/state", []byte("changed-source"), 0600); err != nil {
		t.Fatal(err)
	}
	w := publicationPrivateInspectionWork{original: root, facts: store.PublicationPrivateInspectionFacts{
		Point:  store.NativePreparedPointView{SourceSealSHA256: stable},
		Permit: store.EnginePermit{Runtime: data.EngineRuntime{CopyBytes: limits.Bytes, CopyFiles: limits.Files}},
	}}
	if _, _, err := w.observeOriginal(ctx); !errors.Is(err, errEngineSource) {
		t.Fatal("changed source acquired the old recorded observation", err)
	}
}
