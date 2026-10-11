package data

import (
	"context"
	"slices"
	"time"
)

const SQLiteObservationBudget = 30 * time.Second

// ObservationTarget selects a fixed engine observer, never supplied SQL or shell.
type ObservationTarget struct {
	kind        EngineKind
	sqlite      SQLiteDatabaseBinding
	definitions []SQLiteSchemaDefinition
	allocation  *AllocationReceipt
	spacetime   DatabaseID
}

func SQLiteObservationTarget(b SQLiteDatabaseBinding, definitions []SQLiteSchemaDefinition, allocation *AllocationReceipt) ObservationTarget {
	var detached *AllocationReceipt
	if allocation != nil {
		copy := *allocation
		detached = &copy
	}
	return ObservationTarget{kind: EngineSQLite, sqlite: b, definitions: slices.Clone(definitions), allocation: detached}
}
func SpacetimeDBObservationTarget(id DatabaseID) ObservationTarget {
	return ObservationTarget{kind: EngineSpacetimeDB, spacetime: id}
}
func ObserveEngine(ctx context.Context, target ObservationTarget, budget time.Duration) (EngineObservation, error) {
	if ctx.Err() != nil || budget <= 0 || budget > SQLiteObservationBudget {
		return EngineObservation{}, ErrInvalid
	}
	switch target.kind {
	case EngineSQLite:
		ctx, cancel := context.WithTimeout(ctx, budget)
		defer cancel()
		observed := observeSQLiteSchemaWithAllocation(ctx, target.sqlite, target.definitions, target.allocation)
		return SQLiteObservation(observed), nil
	case EngineSpacetimeDB:
		return EngineObservation{}, ErrUnsupportedEngine
	default:
		return EngineObservation{}, ErrInvalid
	}
}
func ObserveSQLiteSchema(ctx context.Context, b SQLiteDatabaseBinding, definitions []SQLiteSchemaDefinition) SQLiteSchemaObservation {
	return ObserveSQLiteSchemaWithAllocation(ctx, b, definitions, nil)
}
func ObserveSQLiteSchemaWithAllocation(ctx context.Context, b SQLiteDatabaseBinding, definitions []SQLiteSchemaDefinition, receipt *AllocationReceipt) SQLiteSchemaObservation {
	observed, err := ObserveEngine(ctx, SQLiteObservationTarget(b, definitions, receipt), SQLiteObservationBudget)
	if err != nil {
		return SQLiteSchemaObservation{State: Unknown, DatabaseID: b.DatabaseID, ObservedAt: time.Now().UTC(), UnknownReason: "unreadable"}
	}
	sqlite, ok := observed.SQLite()
	if !ok {
		return SQLiteSchemaObservation{State: Unknown, DatabaseID: b.DatabaseID, ObservedAt: time.Now().UTC(), UnknownReason: "unreadable"}
	}
	return sqlite
}
