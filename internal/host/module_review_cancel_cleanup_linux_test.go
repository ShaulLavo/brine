//go:build linux

package host

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublicationReviewCancellationCleansOwnedSourceStaging(t *testing.T) {
	binding, stateRoot := engineSourceFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	copied, err := copyEngineStateSource(ctx, binding, stateRoot, strings.Repeat("f", 32), engineCopyLimits{Bytes: 1 << 20, Files: 128, Depth: 16})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	opened := &nativePreparedReopen{copy: copied}
	cancel()
	if err := finishPublicationReview(opened, nil, nil); err != nil {
		t.Fatal("canceled review could not finish its known owned copy", err)
	}
	if _, err := os.Lstat(filepath.Join(stateRoot, "staging")); !os.IsNotExist(err) {
		t.Fatal("canceled review retained its source copy or created parent", err)
	}
	if _, err := os.Stat(filepath.Join(binding.SourcePath(), "control-db/state")); err != nil {
		t.Fatal("canceled review touched original source", err)
	}
}
