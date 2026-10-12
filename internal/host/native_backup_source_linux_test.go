//go:build linux

package host

import (
	"context"
	"os"
	"testing"
)

func TestNativeCurrentModuleCleanupRequiresStoppedDescriptorConsumer(t *testing.T) {
	preparation, binding := publicationPreparationFixture(t)
	staged, err := stagePublicationArtifact(context.Background(), preparation, binding, publicationArtifactFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = staged.discard() })
	path := staged.path()
	lease := &descriptorLease{artifact: staged, attempted: true}
	module := nativeCurrentModule{candidate: extractedPublicationCandidate{lease: lease}}
	if err := module.close(); err == nil {
		t.Fatal("descriptor disposal uncertainty removed its mounted module")
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatal("unsettled descriptor consumer lost source", err)
	}
	lease.disposed = true
	if err := module.close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("finished capture retained module inspection artifact", err)
	}
}
