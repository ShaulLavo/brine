// Package archive prepares immutable persistent-removal evidence without retiring or deleting data.
package archive

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
	"regexp"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/restore"
)

var ErrRefused = errors.New("archive preparation refused")
var ErrRecovery = errors.New("archive preparation requires inspection")
var appName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
var digest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
var requesterName = regexp.MustCompile(`^deploy:[0-9a-f]{64}$`)

const MaxManifestBytes = 1 << 20
const MaxEvidenceAge = 5 * time.Minute

type Database struct {
	Binding     data.SQLiteDatabaseBinding    `json:"binding"`
	Epochs      []data.ReplicaBinding         `json:"epochs"`
	Allocation  *data.AllocationReceipt       `json:"allocation,omitempty"`
	Written     bool                          `json:"written"`
	Definitions []data.SQLiteSchemaDefinition `json:"definitions"`
	Invariants  []data.RestoreInvariant       `json:"invariants"`
	Retention   []data.RetentionEvidence      `json:"retention"`
}

type Candidate struct {
	App         string                `json:"app"`
	Incarnation data.AppIncarnationID `json:"incarnation"`
	ReleaseID   string                `json:"release_id,omitempty"`
	Databases   []Database            `json:"databases"`
}

type Plan struct {
	ID         string             `json:"id"`
	ArchiveID  data.ArchiveID     `json:"archive_id"`
	Requester  string             `json:"requester"`
	PolicyHash string             `json:"policy_hash"`
	TargetHash string             `json:"target_hash"`
	Generation uint64             `json:"generation"`
	Candidate  Candidate          `json:"candidate"`
	Trees      []data.ArchiveTree `json:"trees"`
	CreatedAt  time.Time          `json:"created_at"`
}

func (p Plan) Fingerprint() (string, error) {
	p.ID = ""
	raw, err := json.Marshal(p)
	if err != nil || len(raw) > MaxManifestBytes {
		return "", ErrRefused
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func (p Plan) Valid() bool {
	id, err := p.Fingerprint()
	if err != nil || id != p.ID || !digest.MatchString(p.ID) || !data.ValidID(string(p.ArchiveID)) || !digest.MatchString(p.PolicyHash) || !digest.MatchString(p.TargetHash) || p.CreatedAt.IsZero() || !requesterName.MatchString(p.Requester) || len(p.Candidate.ReleaseID) > 256 || !appName.MatchString(p.Candidate.App) || !data.ValidID(string(p.Candidate.Incarnation)) || len(p.Candidate.Databases) == 0 || len(p.Candidate.Databases) > 16 || len(p.Trees) == 0 || len(p.Trees) > 16 {
		return false
	}
	roots := map[data.PersistentRoot]bool{}
	rootIdentity := map[data.PersistentRoot]data.ArchiveTree{}
	for i, tree := range p.Trees {
		if !tree.Valid(p.Candidate.Incarnation, p.ArchiveID) || roots[tree.Root] || i > 0 && p.Trees[i-1].Root >= tree.Root {
			return false
		}
		roots[tree.Root] = true
		rootIdentity[tree.Root] = tree
	}
	usedRoots := map[data.PersistentRoot]bool{}
	databases := map[data.DatabaseID]bool{}
	bindings := map[data.ReplicaBindingID]bool{}
	epochs := map[data.ReplicaEpochID]bool{}
	for i, database := range p.Candidate.Databases {
		b := database.Binding
		relative, err := data.RelativeDirectory(b.IncarnationID, b.DatabaseID)
		if err != nil || relative != b.RelativeDirectory || b.IncarnationID != p.Candidate.Incarnation || !roots[b.Root] || databases[b.DatabaseID] || bindings[b.ReplicaBindingID] || !data.ValidID(string(b.ReplicaBindingID)) || i > 0 && p.Candidate.Databases[i-1].Binding.DatabaseID >= b.DatabaseID || len(database.Epochs) == 0 || len(database.Epochs) != len(database.Retention) {
			return false
		}
		declaration := data.SQLiteDatabase{Name: b.Name, PersistentRoot: b.Root, MountPath: b.MountPath, Filename: b.Filename, BackupDestination: database.Epochs[0].Destination.Reference}
		if declaration.Validate() != nil {
			return false
		}
		if database.Allocation == nil || !database.Allocation.Valid() || database.Allocation.DatabaseID != b.DatabaseID || database.Allocation.IncarnationID != b.IncarnationID {
			return false
		}
		if database.Allocation.RootDevice != rootIdentity[b.Root].RootDevice || database.Allocation.RootInode != rootIdentity[b.Root].RootInode {
			return false
		}
		databases[b.DatabaseID] = true
		bindings[b.ReplicaBindingID] = true
		usedRoots[b.Root] = true
		for j, epoch := range database.Epochs {
			prefix, err := data.RemotePrefix(epoch.Destination.BasePrefix, b.IncarnationID, b.DatabaseID, epoch.EpochID)
			if err != nil || epoch.Destination.Validate() != nil || epoch.DatabaseID != b.DatabaseID || epoch.BindingID != b.ReplicaBindingID || epoch.RemotePrefix != prefix || epochs[epoch.EpochID] || j > 0 && database.Epochs[j-1].EpochID >= epoch.EpochID || !database.Retention[j].Admits(epoch.Destination, p.CreatedAt) {
				return false
			}
			epochs[epoch.EpochID] = true
		}
		for _, definition := range database.Definitions {
			if definition.Database != b.Name || !data.ValidMarker(definition.Marker) || !data.ValidCatalogHash(definition.CatalogSHA256) {
				return false
			}
		}
		for _, invariant := range database.Invariants {
			if invariant.Database != b.Name || invariant.Validate() != nil {
				return false
			}
		}
	}
	return len(usedRoots) == len(roots)
}

type Coverage struct {
	DatabaseID        data.DatabaseID              `json:"database_id"`
	WritersQuiescedAt time.Time                    `json:"writers_quiesced_at"`
	Empty             *data.AllocationReceipt      `json:"never_written,omitempty"`
	Observation       data.SQLiteSchemaObservation `json:"observation"`
	Point             *data.SQLiteRestorePoint     `json:"point,omitempty"`
	Receipt           *restore.SQLiteReceipt       `json:"restore_receipt,omitempty"`
}

func (c Coverage) Admits(p Plan, database Database, operation string, now time.Time) bool {
	b := database.Binding
	if c.DatabaseID != b.DatabaseID || c.WritersQuiescedAt.Before(p.CreatedAt) || now.Before(c.WritersQuiescedAt) || now.Sub(c.WritersQuiescedAt) > MaxEvidenceAge || c.Observation.DatabaseID != b.DatabaseID || c.Observation.ObservedAt.Before(c.WritersQuiescedAt) || now.Before(c.Observation.ObservedAt) || c.Observation.UnknownReason != "" {
		return false
	}
	if !database.Written {
		return c.Point == nil && c.Receipt == nil && c.Empty != nil && c.Empty.Valid() && !c.Empty.AllocatedAt.Before(c.WritersQuiescedAt) && !c.Empty.AllocatedAt.After(c.Observation.ObservedAt) && database.Allocation != nil && c.Empty.DatabaseID == b.DatabaseID && c.Empty.IncarnationID == b.IncarnationID && c.Empty.RootDevice == database.Allocation.RootDevice && c.Empty.RootInode == database.Allocation.RootInode && c.Empty.DirectoryDevice == database.Allocation.DirectoryDevice && c.Empty.DirectoryInode == database.Allocation.DirectoryInode && c.Observation.State == data.AllocatedEmpty && c.Observation.Marker == data.EmptyMarker && c.Observation.CatalogSHA256 == data.EmptyCatalogSHA256
	}
	if c.Empty != nil || c.Point == nil || c.Receipt == nil || c.Point.Validate() != nil || !c.Receipt.Valid() || c.Point.Schema != c.Observation || c.Receipt.OperationID != operation+"-"+string(b.DatabaseID) || c.Receipt.ObservedAt.Before(c.Point.RecordedAt) || now.Before(c.Receipt.ObservedAt) || now.Sub(c.Receipt.ObservedAt) > MaxEvidenceAge || c.Point.RecordedAt.Before(c.WritersQuiescedAt) || c.Receipt.Coverage == nil || !c.Receipt.Coverage.CoveredThrough.Equal(c.WritersQuiescedAt) || c.Receipt.LossWindow.State != restore.LossBounded || c.Receipt.Schema.State != restore.SchemaState(c.Observation.State) || c.Receipt.Schema.Marker != c.Observation.Marker || c.Receipt.Schema.CatalogSHA256 != c.Observation.CatalogSHA256 {
		return false
	}
	if len(database.Invariants) > 0 && c.Receipt.InvariantCheck != "passed" {
		return false
	}
	var epoch *data.ReplicaBinding
	for i := range database.Epochs {
		if database.Epochs[i].BindingID == c.Point.BindingID && database.Epochs[i].EpochID == c.Point.EpochID {
			epoch = &database.Epochs[i]
		}
	}
	if epoch == nil {
		return false
	}
	if c.Point.Kind == data.RestorePointLTX {
		source := c.Receipt.Source.LTX
		barrier := c.Point.LTX.Barrier
		return source != nil && !source.Recoverability && source.BindingID == string(epoch.BindingID) && source.Epoch == string(epoch.EpochID) && source.TXID == c.Point.LTX.TXID && source.Barrier != nil && source.Barrier.Succeeded && source.Barrier.ObservedAt.Equal(barrier.ObservedAt) && !barrier.ObservedAt.Before(c.WritersQuiescedAt) && source.Barrier.ReplicaTXID == barrier.ReplicaTXID
	}
	source := c.Receipt.Source.Snapshot
	return source != nil && source.BindingID == string(epoch.BindingID) && source.Epoch == string(epoch.EpochID) && source.PointID == c.Point.ID && source.ObjectKey == path.Join(epoch.RemotePrefix, "restore-points", c.Point.ID, "snapshot.sqlite") && source.ObjectKey == c.Point.Snapshot.ObjectKey && source.SHA256 == c.Point.Snapshot.SHA256 && source.Size == c.Point.Snapshot.Size
}

type Stage string

const (
	Intent            Stage = "intent"
	WritersStopIntent Stage = "writers_stop_intent"
	WritersStopped    Stage = "writers_stopped"
	CoverageIntent    Stage = "coverage_intent"
	CoverageVerified  Stage = "coverage_verified"
	ReplicaStopIntent Stage = "replica_stop_intent"
	Quiesced          Stage = "quiesced"
	Prepared          Stage = "prepared"
)

var stages = []Stage{Intent, WritersStopIntent, WritersStopped, CoverageIntent, CoverageVerified, ReplicaStopIntent, Quiesced, Prepared}

// StageAt identifies the only allowed append-only preparation sequence.
func StageAt(sequence uint64) (Stage, bool) {
	if sequence == 0 || sequence > uint64(len(stages)) {
		return "", false
	}
	return stages[sequence-1], true
}

func Next(from, to Stage) bool {
	for i := 1; i < len(stages); i++ {
		if stages[i-1] == from && stages[i] == to {
			return true
		}
	}
	return false
}

type Operation struct {
	ID        string                 `json:"id"`
	PlanID    string                 `json:"plan_id"`
	ArchiveID data.ArchiveID         `json:"archive_id"`
	Stage     Stage                  `json:"stage"`
	Sequence  uint64                 `json:"sequence"`
	Fences    []data.QuiescenceFence `json:"fences"`
}

type Manifest struct {
	Plan        Plan       `json:"plan"`
	OperationID string     `json:"operation_id"`
	Coverage    []Coverage `json:"coverage"`
	QuiescedAt  time.Time  `json:"quiesced_at"`
	PreparedAt  time.Time  `json:"prepared_at"`
}

func (m Manifest) Valid() bool {
	if !m.Plan.Valid() || !data.ValidID(m.OperationID) || m.QuiescedAt.Before(m.Plan.CreatedAt) || m.PreparedAt.Before(m.QuiescedAt) || len(m.Coverage) != len(m.Plan.Candidate.Databases) {
		return false
	}
	for i, coverage := range m.Coverage {
		if !coverage.Admits(m.Plan, m.Plan.Candidate.Databases[i], m.OperationID, m.PreparedAt) || m.QuiescedAt.Before(coverage.WritersQuiescedAt) {
			return false
		}
	}
	return true
}
