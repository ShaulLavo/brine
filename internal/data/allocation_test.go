//go:build linux

package data

import (
	"context"
	"os"
	"testing"
)

func TestAllocationIsAffirmativeUntouchedIdentity(t *testing.T) {
	b, p := schemaFixture(t)
	receipt, err := CaptureAllocation(b)
	if err != nil {
		t.Fatal(err)
	}
	observation := ObserveSchemaWithAllocation(context.Background(), b, nil, &receipt)
	if observation.State != AllocatedEmpty || observation.Marker != EmptyMarker {
		t.Fatalf("untouched allocation: %+v", observation)
	}
	changed := receipt
	changed.DirectoryInode++
	if o := ObserveSchemaWithAllocation(context.Background(), b, nil, &changed); o.State != Unknown {
		t.Fatal("changed allocation identity accepted")
	}
	if err = os.WriteFile(p+"-wal", nil, 0600); err != nil {
		t.Fatal(err)
	}
	if o := ObserveSchemaWithAllocation(context.Background(), b, nil, &receipt); o.State != Unknown {
		t.Fatal("allocation with WAL accepted")
	}
	if err = os.Remove(p + "-wal"); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(p, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if o := ObserveSchemaWithAllocation(context.Background(), b, nil, &receipt); o.State != Unknown {
		t.Fatal("zero-byte allocation inferred empty")
	}
}
