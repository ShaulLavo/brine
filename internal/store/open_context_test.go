package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenContextCanceledDoesNotCreateControlFiles(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s, err := OpenContext(ctx, root)
	if s != nil || !errors.Is(err, context.Canceled) {
		t.Fatal("canceled startup admitted", err)
	}
	if _, err = os.Stat(filepath.Join(root, "control.db")); !os.IsNotExist(err) {
		t.Fatal("canceled open created state", err)
	}
}
