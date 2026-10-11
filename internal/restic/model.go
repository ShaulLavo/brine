// Package restic transports bounded encrypted archives. Its evidence does not
// authorize native replay, engine startup, data mutation, or fence release.
package restic

import (
	"errors"
	"regexp"
	"time"
)

const Version = "0.19.1"
const ToolPath = "/opt/brine/restic/0.19.1/restic"
const archiveName = "brine-archive.tar"
const manifestLimit = 2 << 20
const jsonLimit = 256 << 10

var (
	ErrInvalid   = errors.New("invalid encrypted archive request")
	ErrIntegrity = errors.New("encrypted archive integrity check failed")
	ErrLimit     = errors.New("encrypted archive limit exceeded")
	ErrTool      = errors.New("protected restic pin unavailable")
	ErrFailed    = errors.New("encrypted archive tool failed")
	ErrUnknown   = errors.New("encrypted archive effect unknown; inspect before retry")
	ErrExpired   = errors.New("encrypted archive credentials expired")
	idPattern    = regexp.MustCompile(`^[0-9a-f]{32}$`)
	hashPattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	pathPattern  = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
)

type Limits struct {
	Bytes        int64
	FileBytes    int64
	ArchiveBytes int64
	Members      int
}

func DefaultLimits() Limits {
	return Limits{Bytes: 64 << 20, FileBytes: 32 << 20, ArchiveBytes: 72 << 20, Members: 8192}
}
func (l Limits) valid() bool {
	return l.Bytes > 0 && l.Bytes <= 8<<30 && l.FileBytes > 0 && l.FileBytes <= l.Bytes && l.ArchiveBytes >= l.Bytes && l.ArchiveBytes <= 9<<30 && l.Members > 0 && l.Members <= 32768
}

type Identity struct {
	BindingID        string `json:"binding_id"`
	EpochID          string `json:"epoch_id"`
	CaptureID        string `json:"capture_id"`
	ModuleSHA256     string `json:"module_sha256"`
	CheckpointSHA256 string `json:"checkpoint_sha256"`
}

func (i Identity) valid() bool {
	return idPattern.MatchString(i.BindingID) && idPattern.MatchString(i.EpochID) && idPattern.MatchString(i.CaptureID) && hashPattern.MatchString(i.ModuleSHA256) && hashPattern.MatchString(i.CheckpointSHA256)
}

// Scope is a prerequisite supplied by the managed admission layer. Syntax
// validation here does not prove destination ownership or operator authority.
type Scope struct {
	BindingID         string    `json:"binding_id"`
	EpochID           string    `json:"epoch_id"`
	DestinationSHA256 string    `json:"destination_sha256"`
	CredentialVersion uint64    `json:"credential_version"`
	RecoveryVersion   uint64    `json:"recovery_version"`
	ExpiresAt         time.Time `json:"expires_at"`
}

func (s Scope) valid() bool {
	return idPattern.MatchString(s.BindingID) && idPattern.MatchString(s.EpochID) && hashPattern.MatchString(s.DestinationSHA256) && s.CredentialVersion > 0 && s.RecoveryVersion > 0 && !s.ExpiresAt.IsZero() && s.ExpiresAt.Location() == time.UTC
}

type TransportPoint struct {
	Identity       Identity `json:"identity"`
	Scope          Scope    `json:"scope"`
	SnapshotID     string   `json:"snapshot_id"`
	ManifestSHA256 string   `json:"manifest_sha256"`
	ArchiveSHA256  string   `json:"archive_sha256"`
	ArchiveBytes   int64    `json:"archive_bytes"`
}

func (p TransportPoint) valid(l Limits) bool {
	return p.Identity.valid() && p.Scope.valid() && p.Identity.BindingID == p.Scope.BindingID && p.Identity.EpochID == p.Scope.EpochID && hashPattern.MatchString(p.SnapshotID) && hashPattern.MatchString(p.ManifestSHA256) && hashPattern.MatchString(p.ArchiveSHA256) && p.ArchiveBytes > 0 && p.ArchiveBytes <= l.ArchiveBytes
}
