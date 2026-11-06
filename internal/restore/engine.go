package restore

import (
	"context"

	"github.com/ShaulLavo/brine/internal/data"
)

// OutputLifetime declares whether a verified file has a consumer after return.
type OutputLifetime uint8

const (
	RetainOutput OutputLifetime = iota
	MetadataOnly
)

// Verification is closed over concrete checks. Engine-specific invariants and
// schema observations cannot be reinterpreted by a different verifier.
type Verification struct {
	kind   data.EngineKind
	sqlite SQLiteRequest
	output OutputLifetime
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
	if r.output != RetainOutput && r.output != MetadataOnly {
		return VerifiedReceipt{}, refuse("invalid_output_lifetime")
	}
	switch r.kind {
	case data.EngineSQLite:
		if v.SQLite == nil {
			return VerifiedReceipt{}, refuse("engine_mismatch")
		}
		var receipt SQLiteReceipt
		var err error
		switch r.output {
		case RetainOutput:
			receipt, err = v.SQLite.Test(ctx, cloneSQLiteRequest(r.sqlite))
		case MetadataOnly:
			receipt, err = v.SQLite.TestMetadata(ctx, cloneSQLiteRequest(r.sqlite))
		}
		if err != nil {
			return VerifiedReceipt{}, err
		}
		return VerifiedReceipt{kind: data.EngineSQLite, sqlite: cloneSQLiteReceipt(receipt)}, nil
	default:
		return VerifiedReceipt{}, refuse("engine_mismatch")
	}
}

func VerifySQLite(ctx context.Context, engine *SQLiteEngine, r SQLiteRequest, output OutputLifetime) (SQLiteReceipt, error) {
	request, err := SQLiteVerification(r)
	if err != nil {
		return SQLiteReceipt{}, err
	}
	request.output = output
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
