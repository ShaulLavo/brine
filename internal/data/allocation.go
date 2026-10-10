package data

import (
	"context"
	"os"
	"path/filepath"
	"time"
)

// AllocationReceipt records a private, empty directory before any writer starts.
// Store history determines whether it remains untouched; missing data alone is
// never sufficient evidence. Device/inode identity is rechecked on every use.
type AllocationReceipt struct {
	RootProof       *RootEvidence    `json:"root_proof,omitempty"`
	MappingProof    *MappingEvidence `json:"mapping_proof,omitempty"`
	DatabaseID      DatabaseID       `json:"database_id"`
	IncarnationID   AppIncarnationID `json:"incarnation_id"`
	RootDevice      uint64           `json:"root_device"`
	RootInode       uint64           `json:"root_inode"`
	DirectoryDevice uint64           `json:"directory_device"`
	DirectoryInode  uint64           `json:"directory_inode"`
	AllocatedAt     time.Time        `json:"allocated_at"`
}

func (r AllocationReceipt) Valid() bool {
	if (r.RootProof == nil) != (r.MappingProof == nil) {
		return false
	}
	if r.RootProof != nil && (r.RootProof.Device != r.RootDevice || r.RootProof.Inode != r.RootInode || !r.RootProof.Admits(r.RootProof.Root, 0) || r.MappingProof.Image != MappingProbeImage || !r.MappingProof.Admits(r.MappingProof.Runtime, *r.RootProof)) {
		return false
	}
	return ValidID(string(r.DatabaseID)) && ValidID(string(r.IncarnationID)) && r.RootDevice != 0 && r.RootInode != 0 && r.DirectoryDevice == r.RootDevice && r.DirectoryInode != 0 && !r.AllocatedAt.IsZero()
}
func CaptureAllocation(b DatabaseBinding) (AllocationReceipt, error) {
	relative, err := RelativeDirectory(b.IncarnationID, b.DatabaseID)
	if err != nil || relative != b.RelativeDirectory {
		return AllocationReceipt{}, ErrInvalid
	}
	source := filepath.Join(string(b.Root), relative)
	if err = verifyPrivateTree(string(b.Root), source); err != nil {
		return AllocationReceipt{}, err
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return AllocationReceipt{}, err
	}
	if len(entries) != 0 {
		return AllocationReceipt{}, ErrInvalid
	}
	rootDevice, rootInode, err := directoryIdentity(string(b.Root))
	if err != nil {
		return AllocationReceipt{}, err
	}
	device, inode, err := directoryIdentity(source)
	if err != nil {
		return AllocationReceipt{}, err
	}
	r := AllocationReceipt{DatabaseID: b.DatabaseID, IncarnationID: b.IncarnationID, RootDevice: rootDevice, RootInode: rootInode, DirectoryDevice: device, DirectoryInode: inode, AllocatedAt: time.Now().UTC()}
	if !r.Valid() {
		return AllocationReceipt{}, ErrInvalid
	}
	return r, nil
}

func ObserveSchemaWithAllocation(ctx context.Context, b DatabaseBinding, definitions []SchemaDefinition, receipt *AllocationReceipt) SchemaObservation {
	o := ObserveSchema(ctx, b, definitions)
	if o.State != Unknown || o.UnknownReason != "missing" || receipt == nil || !receipt.Valid() || receipt.DatabaseID != b.DatabaseID || receipt.IncarnationID != b.IncarnationID || ctx.Err() != nil {
		return o
	}
	fresh, err := CaptureAllocation(b)
	if err != nil || fresh.RootDevice != receipt.RootDevice || fresh.RootInode != receipt.RootInode || fresh.DirectoryDevice != receipt.DirectoryDevice || fresh.DirectoryInode != receipt.DirectoryInode {
		return o
	}
	o.State = AllocatedEmpty
	o.Marker = EmptyMarker
	o.CatalogSHA256 = EmptyCatalogSHA256
	o.UnknownReason = ""
	return o
}

func WriterCompatibleWithAllocations(ctx context.Context, bindings []DatabaseBinding, compatibility []SchemaCompatibility, definitions []SchemaDefinition, allocations map[DatabaseID]AllocationReceipt) bool {
	if !validWriterDeclarations(bindings, compatibility) {
		return false
	}
	for id, receipt := range allocations {
		if !receipt.Valid() || receipt.DatabaseID != id {
			return false
		}
		found := false
		for _, binding := range bindings {
			if binding.DatabaseID == id && binding.IncarnationID == receipt.IncarnationID {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	for _, b := range bindings {
		var receipt *AllocationReceipt
		if r, ok := allocations[b.DatabaseID]; ok {
			receipt = &r
		}
		observation := ObserveSchemaWithAllocation(ctx, b, definitions, receipt)
		found := false
		for _, c := range compatibility {
			if c.Database == b.Name {
				if found || !CompatibleObservation(b.Name, observation, c) {
					return false
				}
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}
