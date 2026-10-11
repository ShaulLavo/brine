package restore

import (
	"context"
	"encoding/json"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

// Source preserves the concrete source and engine tag together. SQLite canonical
// receipts keep their existing source encoding; this tag is the engine boundary.
type Source struct {
	kind      data.EngineKind
	sqlite    SQLiteSource
	spacetime SpacetimeDBSource
}

// SpacetimeDBSource names an encrypted, immutable repository snapshot and member
// manifest. It has no SQLite file, Litestream TXID or upload barrier.
type SpacetimeDBSource struct {
	BindingID          data.ReplicaBindingID `json:"binding_id"`
	EpochID            data.ReplicaEpochID   `json:"epoch_id"`
	CaptureID          string                `json:"capture_id"`
	RepositorySnapshot string                `json:"repository_snapshot"`
	ManifestSHA256     string                `json:"manifest_sha256"`
}

func (s SpacetimeDBSource) Valid() bool {
	return data.ValidID(string(s.BindingID)) && data.ValidID(string(s.EpochID)) && data.ValidID(s.CaptureID) && hash.MatchString(s.RepositorySnapshot) && hash.MatchString(s.ManifestSHA256)
}
func SQLiteSourceVariant(s SQLiteSource) (Source, error) {
	s = cloneSQLiteSource(s)
	id, epoch, err := s.reference()
	if err != nil || !token.MatchString(id) || !token.MatchString(epoch) {
		return Source{}, refuse("invalid_source")
	}
	return Source{kind: data.EngineSQLite, sqlite: s}, nil
}
func SpacetimeDBSourceVariant(s SpacetimeDBSource) (Source, error) {
	if !s.Valid() {
		return Source{}, refuse("invalid_source")
	}
	return Source{kind: data.EngineSpacetimeDB, spacetime: s}, nil
}
func (s Source) Engine() data.EngineKind { return s.kind }
func (s Source) SQLite() (SQLiteSource, bool) {
	return cloneSQLiteSource(s.sqlite), s.kind == data.EngineSQLite
}
func (s Source) SpacetimeDB() (SpacetimeDBSource, bool) {
	return s.spacetime, s.kind == data.EngineSpacetimeDB
}
func (s Source) MarshalJSON() ([]byte, error) {
	switch s.kind {
	case data.EngineSQLite:
		if _, _, err := s.sqlite.reference(); err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"engine": s.kind, "sqlite": s.sqlite})
	case data.EngineSpacetimeDB:
		if !s.spacetime.Valid() {
			return nil, refuse("invalid_source")
		}
		return json.Marshal(map[string]any{"engine": s.kind, "spacetimedb": s.spacetime})
	default:
		return nil, refuse("invalid_source")
	}
}
func (s *Source) UnmarshalJSON(raw []byte) error {
	fields, err := receiptObject(raw, []string{"engine"}, []string{"sqlite", "spacetimedb"})
	if err != nil {
		return err
	}
	kind, err := strictjson.Value[data.EngineKind](fields["engine"])
	if err != nil || !kind.Valid() || len(fields) != 2 || fields[string(kind)] == nil {
		return refuse("invalid_source")
	}
	var decoded Source
	switch kind {
	case data.EngineSQLite:
		if err = receiptSource(fields["sqlite"]); err != nil {
			return err
		}
		value, parseErr := strictjson.Value[SQLiteSource](fields["sqlite"])
		if parseErr != nil {
			return refuse("invalid_source")
		}
		decoded, err = SQLiteSourceVariant(value)
	case data.EngineSpacetimeDB:
		if _, err = strictjson.Object(fields["spacetimedb"], "binding_id", "epoch_id", "capture_id", "repository_snapshot", "manifest_sha256"); err != nil {
			return err
		}
		value, parseErr := strictjson.Value[SpacetimeDBSource](fields["spacetimedb"])
		if parseErr != nil {
			return refuse("invalid_source")
		}
		decoded, err = SpacetimeDBSourceVariant(value)
	default:
		return refuse("invalid_source")
	}
	if err != nil {
		return err
	}
	*s = decoded
	return nil
}

// Verification is closed over concrete checks. Engine-specific invariants and
// schema observations cannot be reinterpreted by a different verifier.
type Verification struct {
	kind      data.EngineKind
	sqlite    SQLiteRequest
	spacetime spacetimeVerification
}

type spacetimeVerification struct {
	source      SpacetimeDBSource
	schema      data.SpacetimeDBSchemaObservation
	operationID string
	budget      time.Duration
}

func SQLiteVerification(r SQLiteRequest) (Verification, error) {
	r = cloneSQLiteRequest(r)
	id, epoch, err := requestReference(r)
	if err != nil || !token.MatchString(id) || !token.MatchString(epoch) {
		return Verification{}, refuse("invalid_source")
	}
	return Verification{kind: data.EngineSQLite, sqlite: r}, nil
}
func SpacetimeDBVerification(operationID string, s SpacetimeDBSource, o data.SpacetimeDBSchemaObservation, budget time.Duration) (Verification, error) {
	if !s.Valid() || o.Validate() != nil || !token.MatchString(operationID) || budget <= 0 || budget > 30*time.Minute {
		return Verification{}, refuse("invalid_verification")
	}
	return Verification{kind: data.EngineSpacetimeDB, spacetime: spacetimeVerification{source: s, schema: o, operationID: operationID, budget: budget}}, nil
}

type VerifiedReceipt struct {
	kind   data.EngineKind
	sqlite SQLiteReceipt
}

func (r VerifiedReceipt) Engine() data.EngineKind { return r.kind }
func (r VerifiedReceipt) SQLite() (SQLiteReceipt, bool) {
	return cloneSQLiteReceipt(r.sqlite), r.kind == data.EngineSQLite
}

type Verifier struct{ SQLite *SQLiteEngine }

func (v Verifier) Test(ctx context.Context, r Verification) (VerifiedReceipt, error) {
	if ctx.Err() != nil || !r.kind.Valid() {
		return VerifiedReceipt{}, refuse("engine_mismatch")
	}
	switch r.kind {
	case data.EngineSQLite:
		if v.SQLite == nil {
			return VerifiedReceipt{}, refuse("engine_mismatch")
		}
		receipt, err := v.SQLite.Test(ctx, cloneSQLiteRequest(r.sqlite))
		if err != nil {
			return VerifiedReceipt{}, err
		}
		return VerifiedReceipt{kind: data.EngineSQLite, sqlite: cloneSQLiteReceipt(receipt)}, nil
	case data.EngineSpacetimeDB:
		if !r.spacetime.source.Valid() || r.spacetime.schema.Validate() != nil || !token.MatchString(r.spacetime.operationID) || r.spacetime.budget <= 0 || r.spacetime.budget > 30*time.Minute {
			return VerifiedReceipt{}, refuse("invalid_verification")
		}
		return VerifiedReceipt{}, data.ErrUnsupportedEngine
	default:
		return VerifiedReceipt{}, refuse("engine_mismatch")
	}
}

func VerifySQLite(ctx context.Context, engine *SQLiteEngine, r SQLiteRequest) (SQLiteReceipt, error) {
	request, err := SQLiteVerification(r)
	if err != nil {
		return SQLiteReceipt{}, err
	}
	receipt, err := (Verifier{SQLite: engine}).Test(ctx, request)
	if err != nil {
		return SQLiteReceipt{}, err
	}
	sqlite, ok := receipt.SQLite()
	if !ok {
		return SQLiteReceipt{}, refuse("engine_mismatch")
	}
	return sqlite, nil
}
