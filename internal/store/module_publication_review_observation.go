package store

import (
	"time"

	"github.com/ShaulLavo/brine/internal/data"
)

type PublicationDescriptorObservation struct {
	Image            string    `json:"image"`
	ManifestDigest   string    `json:"manifest_digest"`
	ImageID          string    `json:"image_id"`
	HelperPID        uint32    `json:"helper_pid"`
	HelperStartTicks uint64    `json:"helper_start_ticks"`
	HelperCgroup     string    `json:"helper_cgroup"`
	StartedAt        time.Time `json:"started_at"`
	FinishedAt       time.Time `json:"finished_at"`
	Disposed         bool      `json:"disposed"`
}

// PublicationReviewObservation is fixed private host output. It is never an
// RPC body or a free proof write; completion joins actual protected preparation
// and the already consumed opaque authenticated restore in the same transaction.
type PublicationReviewObservation struct {
	Descriptor       []byte
	Extraction       PublicationDescriptorObservation
	Preflight        []byte
	PublisherProof   []byte
	Runtime          data.EngineRuntime
	Publisher        string
	HelperPID        uint32
	HelperStartTicks uint64
	HelperCgroup     string
	ChildPID         uint32
	ChildStartTicks  uint64
	StartedAt        time.Time
	FinishedAt       time.Time
	Disposed         bool
}

type PublicationStageObservation struct {
	Process              PublicationReviewObservation `json:"process"`
	NativeDatabase       string                       `json:"native_database"`
	NativePublisher      string                       `json:"native_publisher"`
	ProgramWireHash      string                       `json:"program_wire_hash"`
	SchemaSHA256         string                       `json:"schema_sha256"`
	Receipt              []byte                       `json:"receipt"`
	ReceiptBindingSHA256 string                       `json:"receipt_binding_sha256"`
}
