package restic

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSealRefusesWorkspaceInsideSourceWithoutCreatingArchive(t *testing.T) {
	source, _ := testTree(t)
	workspace := filepath.Join(source, "nested-workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := Seal(context.Background(), source, workspace, testIdentity(), DefaultLimits()); err != ErrInvalid {
		t.Fatal("overlapping source/workspace admitted", err)
	}
	entries, err := os.ReadDir(workspace)
	if err != nil || len(entries) != 0 {
		t.Fatal("overlap refusal changed source")
	}
}

type sourceMutationContext struct {
	context.Context
	source    string
	workspace string
	mutated   bool
	writeErr  error
}

func (c *sourceMutationContext) Err() error {
	if c.mutated {
		return c.Context.Err()
	}
	entries, err := os.ReadDir(c.workspace)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "sealed-") {
			continue
		}
		c.mutated = true
		c.writeErr = os.WriteFile(filepath.Join(c.source, "late-member"), []byte("late bytes"), 0600)
		return c.writeErr
	}
	return c.Context.Err()
}

func TestSealRefusesMemberAddedAfterInitialScan(t *testing.T) {
	source, work := testTree(t)
	ctx := &sourceMutationContext{Context: context.Background(), source: source, workspace: work}
	if _, err := Seal(ctx, source, work, testIdentity(), DefaultLimits()); err != ErrIntegrity {
		t.Fatal("late member produced sealed evidence", err)
	}
	if !ctx.mutated || ctx.writeErr != nil {
		t.Fatal("fixture did not add late member")
	}
	entries, err := os.ReadDir(work)
	if err != nil || len(entries) != 0 {
		t.Fatal("failed seal retained its partial envelope")
	}
}
