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

func TestAllocatedWriterRequiresExactDeclarationsAndReceiptKeys(t *testing.T) {
	b, p := schemaFixture(t)
	receipt, err := CaptureAllocation(b)
	if err != nil {
		t.Fatal(err)
	}
	c := SchemaCompatibility{Database: "main", Startup: "preserve", Accepts: []string{EmptyMarker}}
	other := c
	other.Database = "other"
	receipts := map[DatabaseID]AllocationReceipt{b.DatabaseID: receipt}
	for _, tc := range []struct {
		bindings      []DatabaseBinding
		compatibility []SchemaCompatibility
	}{
		{[]DatabaseBinding{b}, []SchemaCompatibility{c, other}},
		{[]DatabaseBinding{b, b}, []SchemaCompatibility{c}},
		{[]DatabaseBinding{b, b}, []SchemaCompatibility{c, other}},
	} {
		if WriterCompatibleWithAllocations(context.Background(), tc.bindings, tc.compatibility, nil, receipts) {
			t.Fatal("allocated writer accepted non-bijective declaration")
		}
	}
	foreign := DatabaseID("33333333333333333333333333333333")
	receipts[foreign] = receipt
	if WriterCompatibleWithAllocations(context.Background(), []DatabaseBinding{b}, []SchemaCompatibility{c}, nil, receipts) {
		t.Fatal("extra allocation key accepted")
	}
	delete(receipts, foreign)
	// Even an existing valid DB cannot hide a mismatched supplied receipt.
	createSchema(t, p, "PRAGMA user_version=0")
	receipt.DatabaseID = foreign
	receipts[b.DatabaseID] = receipt
	if WriterCompatibleWithAllocations(context.Background(), []DatabaseBinding{b}, []SchemaCompatibility{c}, nil, receipts) {
		t.Fatal("allocation key/identity mismatch accepted")
	}
}
