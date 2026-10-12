package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

type nativeEpochMaterial struct {
	BindingID      data.ReplicaBindingID  `json:"binding_id"`
	EpochID        data.ReplicaEpochID    `json:"epoch_id"`
	RuntimeVersion uint64                 `json:"runtime_version"`
	Allocation     data.AllocationReceipt `json:"allocation"`
	Checkpoint     data.EngineEvidenceRef `json:"checkpoint"`
	Material       data.EngineEvidenceRef `json:"material"`
	TransitionID   string                 `json:"transition_id"`
}

func cloneNativeAllocation(receipt data.AllocationReceipt) data.AllocationReceipt {
	if receipt.RootProof != nil {
		root := *receipt.RootProof
		if root.OperatorQuota != nil {
			quota := *root.OperatorQuota
			root.OperatorQuota = &quota
		}
		receipt.RootProof = &root
	}
	if receipt.MappingProof != nil {
		mapping := *receipt.MappingProof
		receipt.MappingProof = &mapping
	}
	return receipt
}

func readNativeEpochMaterial(ctx context.Context, q dataQuerier, head data.EngineHeadRef, reserved ReservedEngine) (nativeEpochMaterial, error) {
	var material nativeEpochMaterial
	var raw []byte
	err := q.QueryRowContext(ctx, "SELECT canonical FROM data_native_epoch_materials WHERE binding_id=? AND epoch_id=? AND runtime_version=?", head.BindingID, head.EpochID, head.Version).Scan(&raw)
	if err != nil {
		return material, err
	}
	fields := []string{"binding_id", "epoch_id", "runtime_version", "allocation", "checkpoint", "material", "transition_id"}
	if len(raw) > 65536 {
		return material, &IntegrityError{}
	}
	if _, err = strictjson.Object(raw, fields...); err != nil || json.Unmarshal(raw, &material) != nil {
		return material, &IntegrityError{}
	}
	canonical, _ := json.Marshal(material)
	if !bytes.Equal(raw, canonical) || material.BindingID != head.BindingID || material.EpochID != head.EpochID || material.RuntimeVersion != head.Version {
		return material, &IntegrityError{}
	}
	transition, _, err := readEngineTransition(ctx, q, material.TransitionID)
	if err != nil || material.Checkpoint != transition.Common().TargetCheckpoint || material.Material != transition.Common().TargetMaterial ||
		!nativeAllocationMatches(material.Allocation, reserved, transition.Common().TargetTree) {
		return material, &IntegrityError{}
	}
	material.Allocation = cloneNativeAllocation(material.Allocation)
	return material, nil
}

func nativeAllocationMatches(allocation data.AllocationReceipt, reserved ReservedEngine, tree data.EngineTreeSeal) bool {
	return allocation.Valid() && allocation.RootProof != nil && allocation.MappingProof != nil &&
		allocation.DatabaseID == reserved.Database.DatabaseID && allocation.IncarnationID == reserved.Database.IncarnationID &&
		allocation.RootProof.Root == reserved.Database.Root && allocation.RootDevice == tree.Root.Device && allocation.RootInode == tree.Root.Inode &&
		allocation.DirectoryDevice == tree.Directory.Device && allocation.DirectoryInode == tree.Directory.Inode
}

func readInitialEngineAllocation(ctx context.Context, q dataQuerier, reserved ReservedEngine) (data.AllocationReceipt, error) {
	var allocation data.AllocationReceipt
	var raw []byte
	if err := q.QueryRowContext(ctx, "SELECT canonical FROM data_allocations WHERE database_id=?", reserved.Database.DatabaseID).Scan(&raw); err != nil {
		return allocation, err
	}
	if len(raw) > 4096 || json.Unmarshal(raw, &allocation) != nil || !allocation.Valid() || allocation.RootProof == nil || allocation.MappingProof == nil ||
		allocation.DatabaseID != reserved.Database.DatabaseID || allocation.IncarnationID != reserved.Database.IncarnationID || allocation.RootProof.Root != reserved.Database.Root {
		return allocation, &IntegrityError{}
	}
	canonical, err := json.Marshal(allocation)
	if err != nil || !bytes.Equal(canonical, raw) {
		return allocation, errors.Join(&IntegrityError{}, err)
	}
	return cloneNativeAllocation(allocation), nil
}

func projectEngineEpoch(ctx context.Context, q dataQuerier, reserved ReservedEngine, head data.EngineHeadRef) (ReservedEngine, *data.AllocationReceipt, error) {
	if head.EpochID == reserved.Archive.EpochID {
		return reserved, nil, nil
	}
	reservation, archive, err := readNativeEpoch(ctx, q, head.BindingID, head.EpochID)
	if err != nil || archive.Validate(reserved.Database) != nil || reservation.Record().DatabaseID != reserved.Database.DatabaseID || reservation.Record().IncarnationID != reserved.Database.IncarnationID {
		return reserved, nil, &IntegrityError{}
	}
	material, err := readNativeEpochMaterial(ctx, q, head, reserved)
	if err != nil {
		return reserved, nil, &IntegrityError{}
	}
	if err = nativeActivationExists(ctx, q, material.TransitionID, head); err != nil {
		return reserved, nil, err
	}
	reserved.Archive = archive
	return reserved, &material.Allocation, nil
}

func nativeActivationExists(ctx context.Context, q dataQuerier, transitionID string, head data.EngineHeadRef) error {
	transition, cursor, err := readEngineTransition(ctx, q, transitionID)
	common := transition.Common()
	version := common.Base.Head().Version + 1
	if err != nil || head.BindingID != common.Scope.BindingID || head.Version != version || common.TargetRuntimeSHA256 != head.RuntimeSHA256 || common.NewEpochID != head.EpochID {
		return &IntegrityError{}
	}
	switch cursor.Stage {
	case data.EngineTransitionActivated, data.EngineTransitionFinishIntent, data.EngineTransitionCompleted:
		return nil
	default:
		return &IntegrityError{}
	}
}
