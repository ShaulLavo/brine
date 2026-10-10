package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"
)

type rollbackConnector struct{ err error }

func (c rollbackConnector) Connect(context.Context) (driver.Conn, error) {
	return rollbackConnection(c), nil
}
func (c rollbackConnector) Driver() driver.Driver { return rollbackDriver(c) }

type rollbackDriver struct{ err error }

func (d rollbackDriver) Open(string) (driver.Conn, error) { return rollbackConnection(d), nil }

type rollbackConnection struct{ err error }

func (c rollbackConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("fixture does not prepare statements")
}
func (c rollbackConnection) Close() error              { return nil }
func (c rollbackConnection) Begin() (driver.Tx, error) { return rollbackTransaction(c), nil }

type rollbackTransaction struct{ err error }

func (tx rollbackTransaction) Commit() error   { return nil }
func (tx rollbackTransaction) Rollback() error { return tx.err }

func TestRollbackOnExitPreservesCleanupFailure(t *testing.T) {
	primary := errors.New("operation refused")
	cleanup := errors.New("rollback failed")
	for _, rollbackErr := range []error{nil, cleanup} {
		db := sql.OpenDB(rollbackConnector{err: rollbackErr})
		t.Cleanup(func() {
			if err := db.Close(); err != nil {
				t.Error(err)
			}
		})
		tx, err := db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		resultErr := primary
		rollbackOnExit(tx, &resultErr)
		if !errors.Is(resultErr, primary) {
			t.Fatal("lost primary error", resultErr)
		}
		if rollbackErr != nil && !errors.Is(resultErr, cleanup) {
			t.Fatal("lost rollback failure", resultErr)
		}
		// database/sql marks a transaction done even when rollback itself fails.
		// A repeated cleanup must not add the expected ErrTxDone to the result.
		rollbackOnExit(tx, &resultErr)
		if errors.Is(resultErr, sql.ErrTxDone) {
			t.Fatal("finished transaction became failure", resultErr)
		}
	}
}
func TestRollbackOnExitAllowsSuccessfulCommit(t *testing.T) {
	db := sql.OpenDB(rollbackConnector{})
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var resultErr error
	rollbackOnExit(tx, &resultErr)
	if resultErr != nil {
		t.Fatal("committed transaction refused", resultErr)
	}
}
