package store

import (
	"time"

	"github.com/ShaulLavo/brine/internal/data"
)

// NativeInitialOwnerObservation is private provenance content. Decoding it
// grants no authority; retention must join the real stopped preparation and
// current source, allocation, runtime and delivered roles in protected state.
type NativeInitialOwnerObservation struct {
	Format               string                `json:"format"`
	PointID              string                `json:"point_id"`
	OperationID          string                `json:"operation_id"`
	BindingID            data.ReplicaBindingID `json:"binding_id"`
	EpochID              data.ReplicaEpochID   `json:"epoch_id"`
	GenerationID         string                `json:"generation_id"`
	SourceSHA256         string                `json:"source_sha256"`
	SourceSealSHA256     string                `json:"source_seal_sha256"`
	AllocationID         data.DatabaseID       `json:"allocation_id"`
	Runtime              data.EngineRuntime    `json:"runtime"`
	StageDevice          uint64                `json:"stage_device"`
	StageInode           uint64                `json:"stage_inode"`
	HelperPID            uint32                `json:"helper_pid"`
	HelperStartTicks     uint64                `json:"helper_start_ticks"`
	HelperCgroup         string                `json:"helper_cgroup"`
	ChildPID             uint32                `json:"child_pid"`
	ChildStartTicks      uint64                `json:"child_start_ticks"`
	ChildBound           bool                  `json:"child_bound"`
	ChildStopped         bool                  `json:"child_stopped"`
	HelperDisposed       bool                  `json:"helper_disposed"`
	Publisher            string                `json:"publisher"`
	CredentialSHA256     string                `json:"credential_sha256"`
	PublisherProofSHA256 string                `json:"publisher_proof_sha256"`
	PublisherProof       []byte                `json:"publisher_proof"`
	StartedAt            time.Time             `json:"started_at"`
	FinishedAt           time.Time             `json:"finished_at"`
}
