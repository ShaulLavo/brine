package archive

import (
	"encoding/json"
	"slices"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
)

// Database carries one concrete review record. Syntax and fingerprints never
// establish current ownership, exact coverage, or retirement authority.
type Database struct {
	engine data.EngineKind
	sqlite SQLiteDatabase
	native NativeDatabase
}

type NativeDatabase struct {
	Binding         data.SpacetimeDBBinding  `json:"binding"`
	Head            data.EngineHeadRef       `json:"head"`
	State           data.EngineDataState     `json:"state"`
	Allocation      *data.AllocationReceipt  `json:"allocation"`
	Epochs          []data.ArchiveBinding    `json:"epochs"`
	Retention       []data.RetentionEvidence `json:"retention"`
	Dependencies    []data.EngineEvidenceRef `json:"dependencies"`
	TransitionTrees []NativeTransitionTree   `json:"transition_trees"`
}

type NativeTransitionTree struct {
	TransitionID string                     `json:"transition_id"`
	Stage        data.EngineTransitionStage `json:"stage"`
	Seal         data.EngineTreeSeal        `json:"seal"`
}

type DatabaseReference struct {
	Engine            data.EngineKind
	IncarnationID     data.AppIncarnationID
	DatabaseID        data.DatabaseID
	BindingID         data.ReplicaBindingID
	Root              data.PersistentRoot
	RelativeDirectory string
}

func SQLiteDatabaseVariant(record SQLiteDatabase) Database {
	return Database{engine: data.EngineSQLite, sqlite: cloneSQLiteDatabase(record)}
}

func NativeDatabaseVariant(record NativeDatabase) Database {
	return Database{engine: data.EngineSpacetimeDB, native: cloneNativeDatabase(record)}
}

func (d Database) Engine() data.EngineKind { return d.engine }
func (d Database) SQLite() (SQLiteDatabase, bool) {
	return cloneSQLiteDatabase(d.sqlite), d.engine == data.EngineSQLite
}
func (d Database) Native() (NativeDatabase, bool) {
	return cloneNativeDatabase(d.native), d.engine == data.EngineSpacetimeDB
}

func (d Database) Reference() DatabaseReference {
	if d.engine == data.EngineSQLite {
		b := d.sqlite.Binding
		return DatabaseReference{Engine: d.engine, IncarnationID: b.IncarnationID, DatabaseID: b.DatabaseID, BindingID: b.ReplicaBindingID, Root: b.Root, RelativeDirectory: b.RelativeDirectory}
	}
	if d.engine == data.EngineSpacetimeDB {
		b := d.native.Binding
		return DatabaseReference{Engine: d.engine, IncarnationID: b.IncarnationID, DatabaseID: b.DatabaseID, BindingID: b.BindingID, Root: b.Root, RelativeDirectory: b.RelativeDirectory}
	}
	return DatabaseReference{}
}

func (d Database) Written() bool {
	return d.engine == data.EngineSQLite && d.sqlite.Written || d.engine == data.EngineSpacetimeDB && d.native.State == data.EngineInitialized
}

func (d Database) Allocation() *data.AllocationReceipt {
	if d.engine == data.EngineSQLite {
		return cloneAllocation(d.sqlite.Allocation)
	}
	if d.engine == data.EngineSpacetimeDB {
		return cloneAllocation(d.native.Allocation)
	}
	return nil
}

func (d Database) Retention() []data.RetentionEvidence {
	if d.engine == data.EngineSQLite {
		return slices.Clone(d.sqlite.Retention)
	}
	if d.engine == data.EngineSpacetimeDB {
		return slices.Clone(d.native.Retention)
	}
	return nil
}

func (d Database) WithRetention(evidence []data.RetentionEvidence) Database {
	if d.engine == data.EngineSQLite {
		value := cloneSQLiteDatabase(d.sqlite)
		value.Retention = slices.Clone(evidence)
		return SQLiteDatabaseVariant(value)
	}
	if d.engine == data.EngineSpacetimeDB {
		value := cloneNativeDatabase(d.native)
		value.Retention = slices.Clone(evidence)
		return NativeDatabaseVariant(value)
	}
	return Database{}
}

func (d Database) ArchiveBindings() []data.ArchiveBinding {
	if d.engine == data.EngineSpacetimeDB {
		return slices.Clone(d.native.Epochs)
	}
	if d.engine != data.EngineSQLite {
		return nil
	}
	return mapSQLiteArchiveBindings(d.sqlite.Epochs)
}

func mapSQLiteArchiveBindings(epochs []data.ReplicaBinding) []data.ArchiveBinding {
	bindings := make([]data.ArchiveBinding, len(epochs))
	for i, epoch := range epochs {
		bindings[i] = data.ArchiveBinding{BindingID: epoch.BindingID, DatabaseID: epoch.DatabaseID, EpochID: epoch.EpochID, Destination: epoch.Destination, RemotePrefix: epoch.RemotePrefix}
	}
	return bindings
}

func cloneSQLiteDatabase(d SQLiteDatabase) SQLiteDatabase {
	d.Allocation = cloneAllocation(d.Allocation)
	d.Epochs = slices.Clone(d.Epochs)
	d.Definitions = slices.Clone(d.Definitions)
	d.Invariants = slices.Clone(d.Invariants)
	d.Retention = slices.Clone(d.Retention)
	return d
}

func cloneNativeDatabase(d NativeDatabase) NativeDatabase {
	d.Allocation = cloneAllocation(d.Allocation)
	d.Epochs = slices.Clone(d.Epochs)
	d.Retention = slices.Clone(d.Retention)
	d.Dependencies = slices.Clone(d.Dependencies)
	d.TransitionTrees = slices.Clone(d.TransitionTrees)
	return d
}

func cloneAllocation(receipt *data.AllocationReceipt) *data.AllocationReceipt {
	if receipt == nil {
		return nil
	}
	value := *receipt
	value.RootProof = cloneAllocationRoot(value.RootProof)
	if value.MappingProof != nil {
		mapping := *value.MappingProof
		value.MappingProof = &mapping
	}
	return &value
}

func cloneAllocationRoot(root *data.RootEvidence) *data.RootEvidence {
	if root == nil {
		return nil
	}
	value := *root
	if value.OperatorQuota != nil {
		quota := *value.OperatorQuota
		value.OperatorQuota = &quota
	}
	return &value
}

func verifyDatabaseAllocation(d Database, id data.ArchiveID, archived bool) error {
	concrete, sqlite := d.SQLite()
	if !sqlite || concrete.Allocation == nil {
		return ErrRefused
	}
	if archived {
		return data.VerifyArchivedAllocation(concrete.Binding, *concrete.Allocation, id)
	}
	return data.VerifyArchiveAllocation(concrete.Binding, *concrete.Allocation)
}

func (d Database) MarshalJSON() ([]byte, error) {
	if d.engine == data.EngineSQLite {
		return json.Marshal(d.sqlite)
	}
	if d.engine != data.EngineSpacetimeDB {
		return nil, ErrRefused
	}
	return json.Marshal(struct {
		Engine data.EngineKind `json:"engine"`
		Native NativeDatabase  `json:"spacetimedb"`
	}{d.engine, d.native})
}

type Coverage struct {
	engine data.EngineKind
	sqlite SQLiteCoverage
	native NativeCoverage
}

type NativeCoverage struct {
	DatabaseID           data.DatabaseID        `json:"database_id"`
	Head                 data.EngineHeadRef     `json:"head"`
	Point                data.EngineEvidenceRef `json:"point"`
	FenceID              data.FenceID           `json:"fence_id"`
	FenceOwnerOpID       string                 `json:"fence_owner_op_id"`
	SourceWitnessSHA256  string                 `json:"source_witness_sha256"`
	CheckpointSHA256     string                 `json:"checkpoint_sha256"`
	RestoreReceiptSHA256 string                 `json:"restore_receipt_sha256"`
	StopProofSHA256      string                 `json:"stop_proof_sha256"`
	WritersQuiescedAt    time.Time              `json:"writers_quiesced_at"`
	VerifiedAt           time.Time              `json:"verified_at"`
	ExpiresAt            time.Time              `json:"expires_at"`
}

func SQLiteCoverageVariant(record SQLiteCoverage) Coverage {
	return Coverage{engine: data.EngineSQLite, sqlite: record}
}
func NativeCoverageVariant(record NativeCoverage) Coverage {
	return Coverage{engine: data.EngineSpacetimeDB, native: record}
}
func (c Coverage) Engine() data.EngineKind        { return c.engine }
func (c Coverage) SQLite() (SQLiteCoverage, bool) { return c.sqlite, c.engine == data.EngineSQLite }
func (c Coverage) Native() (NativeCoverage, bool) {
	return c.native, c.engine == data.EngineSpacetimeDB
}
func (c Coverage) WritersQuiescedAt() time.Time {
	if c.engine == data.EngineSQLite {
		return c.sqlite.WritersQuiescedAt
	}
	if c.engine == data.EngineSpacetimeDB {
		return c.native.WritersQuiescedAt
	}
	return time.Time{}
}

func (c Coverage) Admits(p Plan, d Database, operation string, now time.Time) bool {
	if c.engine != d.engine {
		return false
	}
	if c.engine == data.EngineSQLite {
		return c.sqlite.Admits(p, d.sqlite, operation, now)
	}
	return false
}

func (c Coverage) MarshalJSON() ([]byte, error) {
	if c.engine == data.EngineSQLite {
		return json.Marshal(c.sqlite)
	}
	if c.engine != data.EngineSpacetimeDB {
		return nil, ErrRefused
	}
	return json.Marshal(struct {
		Engine data.EngineKind `json:"engine"`
		Native NativeCoverage  `json:"spacetimedb"`
	}{c.engine, c.native})
}
