package restore

import (
	"context"

	"github.com/ShaulLavo/brine/internal/data"
)

// Verification is closed over concrete checks. Engine-specific invariants and
// schema observations cannot be reinterpreted by a different verifier.
type Verification struct {
	kind   data.EngineKind
	sqlite SQLiteRequest
}

func SQLiteVerification(r SQLiteRequest) (Verification, error) {
	r = cloneSQLiteRequest(r)
	id, epoch, err := requestReference(r)
	if err != nil || !token.MatchString(id) || !token.MatchString(epoch) {
		return Verification{}, refuse("invalid_source")
	}
	return Verification{kind: data.EngineSQLite, sqlite: r}, nil
}

type VerifiedReceipt struct {
	kind   data.EngineKind
	sqlite SQLiteReceipt
}

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
