package data

import "time"

// RetentionEvidence is an operator control-plane verification, never a claim
// inferred from S3 data credentials or a running replica process. Policy protects
// the record and its freshness budget; it binds the exact bucket/base prefix.
type RetentionEvidence struct {
	Destination        BackupDestinationRef `json:"destination" toml:"destination"`
	Endpoint           string               `json:"endpoint" toml:"endpoint"`
	Bucket             string               `json:"bucket" toml:"bucket"`
	BasePrefix         string               `json:"base_prefix" toml:"base_prefix"`
	VerificationID     string               `json:"verification_id" toml:"verification_id"`
	VerifiedAt         string               `json:"verified_at" toml:"verified_at"`
	FreshnessSeconds   uint64               `json:"freshness_seconds" toml:"freshness_seconds"`
	NoObjectExpiration bool                 `json:"no_object_expiration" toml:"no_object_expiration"`
}

func (e RetentionEvidence) Valid() bool {
	at, err := time.Parse(time.RFC3339Nano, e.VerifiedAt)
	return err == nil && !at.IsZero() && ValidID(e.VerificationID) && e.FreshnessSeconds > 0 && e.FreshnessSeconds <= 30*24*60*60
}
func (e RetentionEvidence) Admits(destination Destination, now time.Time) bool {
	if !e.Valid() || !e.NoObjectExpiration || e.Destination != destination.Reference || e.Endpoint != destination.Endpoint || e.Bucket != destination.Bucket || e.BasePrefix != destination.BasePrefix {
		return false
	}
	at, _ := time.Parse(time.RFC3339Nano, e.VerifiedAt)
	return !now.Before(at) && now.Sub(at) <= time.Duration(e.FreshnessSeconds)*time.Second //nolint:gosec // Valid bounds freshness to at most 30 days.
}

// MappingEvidence is a measured Podman keep-id bind-mount probe. Merely having
// subuid ranges or a supported version cannot manufacture this affirmative fact.
type MappingEvidence struct {
	Runtime            RuntimeIdentity `json:"runtime"`
	RunnerUID          uint32          `json:"runner_uid"`
	RunnerGID          uint32          `json:"runner_gid"`
	Image              string          `json:"image"`
	Root               PersistentRoot  `json:"root"`
	Device             uint64          `json:"device"`
	KeepID             bool            `json:"keep_id"`
	PrivateModes       bool            `json:"private_modes"`
	HostReadWrite      bool            `json:"host_read_write"`
	ContainerReadWrite bool            `json:"container_read_write"`
	ObservedAt         time.Time       `json:"observed_at"`
}

func (e MappingEvidence) Admits(runtime RuntimeIdentity, root RootEvidence) bool {
	return e.Runtime == runtime && runtime.Validate() == nil && e.RunnerUID != 0 && e.RunnerGID != 0 && e.Root == root.Root && e.Device == root.Device && e.KeepID && e.PrivateModes && e.HostReadWrite && e.ContainerReadWrite && !e.ObservedAt.IsZero()
}
