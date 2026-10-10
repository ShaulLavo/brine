//go:build linux

package data

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestUsageReportsLogicalBytesAndRefusesUnknownTraversal(t *testing.T) {
	binding, path := schemaFixture(t)
	if err := os.WriteFile(path, []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}
	usage, err := ObserveUsage(context.Background(), binding)
	if err != nil || usage.Bytes != 7 || usage.Files != 1 {
		t.Fatalf("usage %+v %v", usage, err)
	}
	directory := filepath.Dir(path)
	if err = os.Symlink(path, filepath.Join(directory, "other")); err != nil {
		t.Fatal(err)
	}
	if _, err = ObserveUsage(context.Background(), binding); err == nil {
		t.Fatal("symlink counted as data")
	}
	if err = os.Remove(filepath.Join(directory, "other")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = ObserveUsage(ctx, binding); err == nil {
		t.Fatal("cancelled usage became zero")
	}
	if err = os.Chmod(path, 0644); err != nil { //nolint:gosec // Intentional unsafe-permission fixture proves fail-closed refusal.
		t.Fatal(err)
	}
	if _, err = ObserveUsage(context.Background(), binding); err == nil {
		t.Fatal("foreign mode counted")
	}
}

func TestUsageTraversalIsBounded(t *testing.T) {
	for _, kind := range []string{"depth", "entries"} {
		t.Run(kind, func(t *testing.T) {
			binding, path := schemaFixture(t)
			directory := filepath.Dir(path)
			if kind == "depth" {
				for i := 0; i < 17; i++ {
					directory = filepath.Join(directory, "nested")
					if err := os.Mkdir(directory, 0700); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				for i := 0; i < 4097; i++ {
					if err := os.WriteFile(filepath.Join(directory, strconv.Itoa(i)), nil, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			usage, err := ObserveUsage(context.Background(), binding)
			if err == nil || usage != (StorageUsage{}) {
				t.Fatal("unbounded traversal produced known usage", usage, err)
			}
		})
	}
}
