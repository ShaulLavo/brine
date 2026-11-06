package sqliteprep

import (
	"reflect"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/restore"
)

type Location struct {
	Device  uint64 `json:"device"`
	Inode   uint64 `json:"inode"`
	MountID uint64 `json:"mount_id"`
	UID     uint32 `json:"uid"`
	GID     uint32 `json:"gid"`
	Mode    uint32 `json:"mode"`
}

func (l Location) Validate() error {
	if l.Device == 0 || l.Inode == 0 || l.MountID == 0 || l.Mode != 0700 {
		return ErrInvalid
	}
	return nil
}

type Presence string

const (
	Absent  Presence = "absent"
	Regular Presence = "regular"
)

// Member records known presence and regular-file type. Absence has no
// file metadata. ProofSHA256 is the host's existing stopped-file content proof.
type Member struct {
	Presence    Presence `json:"presence"`
	Device      uint64   `json:"device"`
	Inode       uint64   `json:"inode"`
	MountID     uint64   `json:"mount_id"`
	UID         uint32   `json:"uid"`
	GID         uint32   `json:"gid"`
	Mode        uint32   `json:"mode"`
	Links       uint64   `json:"links"`
	Size        int64    `json:"size"`
	MtimeNS     int64    `json:"mtime_ns"`
	ProofSHA256 string   `json:"proof_sha256"`
}

func validMember(m Member, l Location, authoritative bool) bool {
	if m.Presence == Absent {
		return m == (Member{Presence: Absent})
	}
	if m.Presence != Regular || m.Device != l.Device || m.MountID != l.MountID || m.Inode == 0 || m.UID != l.UID || m.GID != l.GID || m.Mode != 0600 || m.Links != 1 || m.Size < 0 || m.Size > 1<<40 || m.MtimeNS <= 0 {
		return false
	}
	if authoritative {
		return data.ValidCatalogHash(m.ProofSHA256)
	}
	return m.ProofSHA256 == "" // No authoritative content or inode claim for SHM.
}

type StoppedBundle struct {
	Location Location `json:"location"`
	DB       Member   `json:"db"`
	WAL      Member   `json:"wal"`
	SHM      Member   `json:"shm"`
	Journal  Member   `json:"journal"`
}

func (b StoppedBundle) Validate() error {
	if b.Location.Validate() != nil || !validMember(b.DB, b.Location, true) || !validMember(b.WAL, b.Location, true) || !validMember(b.Journal, b.Location, true) || !validMember(b.SHM, b.Location, false) {
		return ErrInvalid
	}
	if b.DB.Presence == Absent && (b.WAL.Presence != Absent || b.Journal.Presence != Absent || b.SHM.Presence != Absent) {
		return ErrInvalid
	}
	return nil
}

// Matches explicitly permits a safe absent/present SHM transition or SHM rebuild.
// The DB, WAL, rollback journal and directory must remain exactly unchanged.
func (b StoppedBundle) Matches(other StoppedBundle) bool {
	return b.Validate() == nil && other.Validate() == nil && b.Location == other.Location && b.DB == other.DB && b.WAL == other.WAL && b.Journal == other.Journal
}
func (b StoppedBundle) CaptureFits() bool {
	return b.Validate() == nil && b.DB.Size <= MaxCaptureBytes && b.WAL.Size <= MaxCaptureBytes-b.DB.Size && b.Journal.Size <= MaxCaptureBytes-b.DB.Size-b.WAL.Size
}

type RemoteSource struct {
	Origin     string                  `json:"origin"`
	Point      data.SQLiteRestorePoint `json:"point"`
	Cutoff     time.Time               `json:"cutoff"`
	UploadedAt time.Time               `json:"uploaded_at"`
}

func (s RemoteSource) Validate() error {
	if !data.ValidID(s.Origin) || s.Point.ID != s.Origin || s.Point.Validate() != nil || !utc(s.Point.RecordedAt) || !utc(s.Point.Schema.ObservedAt) || !utc(s.Cutoff) || !utc(s.UploadedAt) || s.UploadedAt.Before(s.Cutoff) || s.Point.RecordedAt.Before(s.UploadedAt) {
		return ErrInvalid
	}
	if s.Point.LTX != nil && (!utc(s.Point.LTX.Barrier.ObservedAt) || s.Point.LTX.Barrier.ObservedAt.Before(s.Cutoff) || s.Point.LTX.Barrier.ObservedAt.After(s.UploadedAt)) {
		return ErrInvalid
	}
	if s.Point.Snapshot != nil && s.Point.Snapshot.Size > MaxCaptureBytes {
		return ErrLimit
	}
	return nil
}
func (s RemoteSource) MatchesScope(scope Scope) bool {
	if s.Validate() != nil || s.Point.BindingID != scope.BindingID || s.Point.EpochID != scope.EpochID || s.Point.Schema.DatabaseID != scope.DatabaseID || !sameSchema(s.Point.Schema, pointSchema(scope.Schema)) {
		return false
	}
	return s.Point.Snapshot == nil || s.Point.Snapshot.ObjectKey == SnapshotKey(scope, s.Origin)
}
func SnapshotKey(s Scope, id string) string {
	return s.RemotePrefix + "restore-points/" + id + "/snapshot.sqlite"
}
func pointSchema(s data.SQLiteSchemaObservation) data.SQLiteSchemaObservation {
	if s.State == data.AllocatedEmpty {
		s.State = data.VerifiedEmpty
	}
	return s
}
func (s RemoteSource) RestoreSource() restore.SQLiteSource {
	p := s.Point
	if p.LTX != nil {
		b := p.LTX.Barrier
		return restore.SQLiteSource{Kind: restore.LitestreamLTX, LTX: &restore.LTXSource{BindingID: string(p.BindingID), Epoch: string(p.EpochID), TXID: p.LTX.TXID, Barrier: &restore.BarrierReceipt{BindingID: string(b.BindingID), Epoch: string(b.EpochID), TXID: b.TXID, ReplicaTXID: b.ReplicaTXID, ObservedAt: b.ObservedAt, Succeeded: true}}}
	}
	if p.Snapshot != nil {
		return restore.SQLiteSource{Kind: restore.SQLiteSnapshot, Snapshot: &restore.SnapshotSource{BindingID: string(p.BindingID), Epoch: string(p.EpochID), PointID: p.ID, ObjectKey: p.Snapshot.ObjectKey, SHA256: p.Snapshot.SHA256, Size: p.Snapshot.Size}}
	}
	return restore.SQLiteSource{}
}
func sameSource(a, b RemoteSource) bool { return reflect.DeepEqual(a, b) }

// StoppedRecord and CoverageRecord are qualified read projections, not authority
// tokens. Journal.Inspect must derive them from the real protected journal and
// revalidate the coverage/baseline relationship, never accept these from input.
type StoppedRecord struct {
	PreparationID string
	Scope         Scope
	Fence         data.QuiescenceFence
	Bundle        StoppedBundle
}
type CoverageRecord struct {
	Source                RemoteSource
	Scope                 Scope
	Fence                 data.QuiescenceFence
	BaselinePreparationID string
	BaselineSHA256        string
}
type Lineage struct {
	Previous    Intent
	Terminal    bool
	Unconsumed  bool
	OwnStopped  bool
	OwnUploaded bool
	Baseline    *StoppedRecord
	Coverage    *CoverageRecord
}
type Origins struct {
	BaselineFrom   string
	CoverageOrigin string
	Baseline       *StoppedBundle
	Source         *RemoteSource
}

func ResolveOrigins(l Lineage) (Origins, error) {
	if l.Previous.Validate() != nil || !l.Terminal || !l.Unconsumed {
		return Origins{}, ErrInvalid
	}
	result := Origins{}
	if l.Baseline != nil {
		if err := resolveBaseline(l, &result); err != nil {
			return Origins{}, err
		}
	}
	if l.Baseline == nil && (l.OwnStopped || l.Previous.BaselineFrom != "") {
		return Origins{}, ErrInvalid
	}
	if l.Coverage != nil {
		if err := resolveCoverage(l, &result); err != nil {
			return Origins{}, err
		}
	}
	if l.Coverage == nil && l.Previous.CoverageOrigin != "" {
		return Origins{}, ErrInvalid
	}
	return result, nil
}
func resolveBaseline(l Lineage, out *Origins) error {
	b := l.Baseline
	wanted := l.Previous.BaselineFrom
	if l.OwnStopped {
		wanted = l.Previous.Input.PreparationID
	}
	if b.PreparationID != wanted || b.Bundle.Validate() != nil || b.Bundle.Location != l.Previous.Location || b.Fence != l.Previous.Fence || !reflect.DeepEqual(b.Scope, l.Previous.Scope) {
		return ErrInvalid
	}
	out.BaselineFrom = b.PreparationID
	bundle := b.Bundle
	out.Baseline = &bundle
	return nil
}
func resolveCoverage(l Lineage, out *Origins) error {
	c := l.Coverage
	wanted := l.Previous.CoverageOrigin
	if l.OwnUploaded {
		wanted = l.Previous.Input.PreparationID
	}
	if c.Source.Origin != wanted || !c.Source.MatchesScope(l.Previous.Scope) || c.Fence != l.Previous.Fence || !reflect.DeepEqual(c.Scope, l.Previous.Scope) || c.Source.Cutoff.Before(l.Previous.ClaimedAt) && l.OwnUploaded {
		return ErrInvalid
	}
	if out.Baseline == nil {
		return nil
	} // Uploaded alone is not stopped-source coverage.
	digest, err := Fingerprint(*out.Baseline)
	if err != nil || c.BaselinePreparationID != out.BaselineFrom || c.BaselineSHA256 != digest {
		return ErrInvalid
	}
	source := c.Source
	out.Source = &source
	out.CoverageOrigin = source.Origin
	return nil
}
