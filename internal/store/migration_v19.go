package store

import (
	"context"
	"database/sql"
)

func migrateManagedLifecycle(ctx context.Context, tx *sql.Tx) error {
	for _, migrate := range []func(context.Context, *sql.Tx) error{migrateManagedPreparations, migrateManagedFrontendPermits, migrateManagedDesired, migrateManagedRoutes} {
		if err := migrate(ctx, tx); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, "UPDATE schema_version SET version=19")
	return err
}
