// Package restore verifies remote backups in fresh private operation directories.
// It has no live database, local replica metadata or publication input.
package restore

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/localexec"
)

const (
	LitestreamVersion   = "0.5.17"
	LitestreamPath      = localexec.LitestreamPath
	SnapshotToolVersion = "brine-s3-snapshot-v1"
)

type SourceKind string

const (
	LitestreamLTX  SourceKind = "litestream_ltx"
	SQLiteSnapshot SourceKind = "sqlite_snapshot"
)

type RestoreSource struct {
	Kind     SourceKind      `json:"kind"`
	LTX      *LTXSource      `json:"litestream_ltx,omitempty"`
	Snapshot *SnapshotSource `json:"sqlite_snapshot,omitempty"`
}
type LTXSource struct {
	BindingID string          `json:"binding_id"`
	Epoch     string          `json:"epoch"`
	TXID      uint64          `json:"txid"`
	Barrier   *BarrierReceipt `json:"barrier,omitempty"`
}
type SnapshotSource struct {
	BindingID string `json:"binding_id"`
	Epoch     string `json:"epoch"`
	PointID   string `json:"point_id"`
	ObjectKey string `json:"object_key"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
}

// BarrierReceipt is supplied by the committed sync -wait operation, not inferred
// from local status or from the restore itself.
type BarrierReceipt struct {
	BindingID   string    `json:"binding_id"`
	Epoch       string    `json:"epoch"`
	TXID        uint64    `json:"txid"`
	ReplicaTXID uint64    `json:"replica_txid"`
	ObservedAt  time.Time `json:"observed_at"`
	Succeeded   bool      `json:"succeeded"`
}

type Destination struct {
	Endpoint  string
	Region    string
	Bucket    string
	Prefix    string
	PathStyle bool
}
type Binding struct {
	ID, Epoch, CredentialRef string
	Destination              Destination
}
type BindingReader interface {
	ReadBinding(context.Context, string, string) (Binding, error)
}
type BindingReaderFunc func(context.Context, string, string) (Binding, error)

func (f BindingReaderFunc) ReadBinding(ctx context.Context, id, epoch string) (Binding, error) {
	return f(ctx, id, epoch)
}

type Credentials struct {
	AccessKey             string `json:"-"`
	SecretKey             string `json:"-"`
	SessionToken          string `json:"-"`
	ReceivedAt, ExpiresAt time.Time
}

func (Credentials) String() string   { return "[redacted credentials]" }
func (Credentials) GoString() string { return "[redacted credentials]" }

type CredentialReader interface {
	ReadCredentials(context.Context, string) (Credentials, error)
}
type CredentialReaderFunc func(context.Context, string) (Credentials, error)

func (f CredentialReaderFunc) ReadCredentials(ctx context.Context, ref string) (Credentials, error) {
	return f(ctx, ref)
}

type SchemaState string

const (
	VerifiedEmpty  SchemaState = "verified_empty"
	VerifiedSchema SchemaState = "verified_schema"
	Unknown        SchemaState = "unknown"
)

type SchemaObservation struct {
	State         SchemaState `json:"state"`
	Marker        string      `json:"marker"`
	CatalogSHA256 string      `json:"catalog_sha256"`
}

// SchemaObserver adapts P04-01's fixed observer. It must independently verify the
// fixed marker structure and normative catalog hash in this read-only transaction.
// There is deliberately no permissive built-in observer while that lane lands.
type SchemaObserver interface {
	Observe(context.Context, *sql.Tx) (SchemaObservation, error)
}

type InvariantKind string

const (
	RowCount     InvariantKind = "row_count"
	NonNull      InvariantKind = "non_null"
	IntegerRange InvariantKind = "integer_range"
)

type Invariant struct {
	Kind             InvariantKind
	Table, Column    string
	Count            int64
	Minimum, Maximum int64
}
type Sentinel struct {
	Table       string    `json:"table"`
	Sequence    int64     `json:"sequence"`
	Marker      string    `json:"marker"`
	CommittedAt time.Time `json:"committed_at"`
}
type Request struct {
	OperationID    string
	Source         RestoreSource
	Budget         time.Duration
	ExpectedSchema SchemaObservation
	Invariants     []Invariant
	Sentinel       *Sentinel
	Coverage       *CoverageEvidence
}

// CoverageEvidence comes from independently recorded commit coverage, not an
// object timestamp, recovered sentinel time or upload barrier alone.
// Its source identity must match the exact restored point.
type CoverageEvidence struct {
	Source         RestoreSource `json:"source"`
	CoveredThrough time.Time     `json:"covered_through"`
}
type LossState string

const (
	LossUnknown LossState = "unknown"
	LossBounded LossState = "bounded"
)

type LossWindow struct {
	State  LossState `json:"state"`
	From   time.Time `json:"from,omitempty"`
	To     time.Time `json:"to,omitempty"`
	Reason string    `json:"reason"`
}
type Receipt struct {
	OperationID      string            `json:"operation_id"`
	Source           RestoreSource     `json:"source"`
	ToolVersion      string            `json:"tool_version"`
	RequestedTXID    uint64            `json:"requested_txid,omitempty"`
	RecoveredTXID    uint64            `json:"recovered_txid,omitempty"`
	ObservedAt       time.Time         `json:"observed_at"`
	Schema           SchemaObservation `json:"schema"`
	Sentinel         *Sentinel         `json:"sentinel,omitempty"`
	Barrier          *BarrierReceipt   `json:"upload_barrier,omitempty"`
	LossWindow       LossWindow        `json:"loss_window"`
	Coverage         *CoverageEvidence `json:"coverage,omitempty"`
	IntegrityCheck   string            `json:"integrity_check"`
	ForeignKeyCheck  string            `json:"foreign_key_check"`
	InvariantCheck   string            `json:"invariant_check"`
	PositionEvidence string            `json:"position_evidence,omitempty"`
	DatabasePath     string            `json:"-"`
}

type Error struct{ Code string }

func (e *Error) Error() string { return "restore test: " + e.Code }
func refuse(code string) error { return &Error{Code: code} }

var token = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`)
var identifier = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]{0,127}$`)
var hash = regexp.MustCompile(`^[0-9a-f]{64}$`)
var schemaMarker = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

func (s RestoreSource) reference() (string, string, error) {
	switch s.Kind {
	case LitestreamLTX:
		if s.LTX == nil || s.Snapshot != nil || s.LTX.TXID == 0 {
			return "", "", refuse("invalid_source")
		}
		if b := s.LTX.Barrier; b == nil || !b.Succeeded || b.BindingID != s.LTX.BindingID || b.Epoch != s.LTX.Epoch || b.TXID != s.LTX.TXID || b.ReplicaTXID < b.TXID || b.ObservedAt.IsZero() {
			return "", "", refuse("invalid_barrier")
		}
		return s.LTX.BindingID, s.LTX.Epoch, nil
	case SQLiteSnapshot:
		if s.Snapshot == nil || s.LTX != nil || !token.MatchString(s.Snapshot.PointID) || !hash.MatchString(s.Snapshot.SHA256) || s.Snapshot.Size <= 0 || !validKey(s.Snapshot.ObjectKey) {
			return "", "", refuse("invalid_source")
		}
		return s.Snapshot.BindingID, s.Snapshot.Epoch, nil
	default:
		return "", "", refuse("invalid_source")
	}
}
func validKey(key string) bool {
	if key == "" || len(key) > 1024 {
		return false
	}
	for _, part := range strings.Split(key, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
		for _, r := range part {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._-", r)) {
				return false
			}
		}
	}
	return true
}
func validSchema(s SchemaObservation) bool {
	if !hash.MatchString(s.CatalogSHA256) {
		return false
	}
	if s.State == VerifiedEmpty {
		return s.Marker == "brine-empty-v1" && s.CatalogSHA256 == "4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945"
	}
	return s.State == VerifiedSchema && s.Marker != "brine-empty-v1" && schemaMarker.MatchString(s.Marker)
}
func validateChecks(r Request) error {
	if len(r.Invariants) > 64 || !validSchema(r.ExpectedSchema) {
		return refuse("invalid_verification")
	}
	for _, check := range r.Invariants {
		if !identifier.MatchString(check.Table) {
			return refuse("invalid_invariant")
		}
		switch check.Kind {
		case RowCount:
			if check.Count < 0 || check.Column != "" || check.Minimum != 0 || check.Maximum != 0 {
				return refuse("invalid_invariant")
			}
		case NonNull:
			if !identifier.MatchString(check.Column) || check.Count != 0 || check.Minimum != 0 || check.Maximum != 0 {
				return refuse("invalid_invariant")
			}
		case IntegerRange:
			if !identifier.MatchString(check.Column) || check.Count != 0 || check.Minimum > check.Maximum {
				return refuse("invalid_invariant")
			}
		default:
			return refuse("invalid_invariant")
		}
	}
	if s := r.Sentinel; s != nil && (!identifier.MatchString(s.Table) || s.Sequence < 0 || s.Marker == "" || len(s.Marker) > 256 || s.CommittedAt.IsZero()) {
		return refuse("invalid_sentinel")
	}
	if r.ExpectedSchema.State == VerifiedEmpty && (r.Sentinel != nil || len(r.Invariants) != 0) {
		return refuse("invalid_empty_proof")
	}
	return nil
}
func txidString(txid uint64) string { return fmt.Sprintf("%016x", txid) }

func samePoint(first, second RestoreSource) bool {
	if _, _, err := first.reference(); err != nil {
		return false
	}
	if first.Kind != second.Kind {
		return false
	}
	if first.Snapshot != nil {
		return *first.Snapshot == *second.Snapshot
	}
	return first.LTX.BindingID == second.LTX.BindingID && first.LTX.Epoch == second.LTX.Epoch && first.LTX.TXID == second.LTX.TXID
}
