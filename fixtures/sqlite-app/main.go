// sqlite-app is a schema-preserving persistent-data drill fixture.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
)

const catalogHash = "f7a92d3e7a27c89c3afd455307ec78b2417b6e643aefbcf1c7c33c3c59838341"

func verifySchema(ctx context.Context, tx *sql.Tx) error {
	observation := data.ObserveSchemaTransaction(ctx, tx, "main", []data.SchemaDefinition{{Database: "main", Marker: "fixture-v1", CatalogSHA256: catalogHash}})
	if observation.State != data.VerifiedSchema {
		return errors.New("reviewed fixture-v1 schema required")
	}
	return nil
}

func openDatabase(ctx context.Context, path string) (*sql.DB, error) {
	u := url.URL{Scheme: "file", Path: path}
	query := u.Query()
	query.Set("mode", "rw") // Never allocate an absent database.
	query.Add("_pragma", "busy_timeout(5000)")
	u.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err = checkSchema(ctx, db); err == nil {
		var mode string
		err = db.QueryRowContext(ctx, "PRAGMA journal_mode=WAL").Scan(&mode)
		if err == nil && mode != "wal" {
			err = errors.New("WAL required")
		}
	}
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func checkSchema(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = verifySchema(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

type commitRow struct {
	Sequence    int64  `json:"sequence"`
	Marker      string `json:"marker"`
	CommittedAt string `json:"committed_at"`
}

func routes(db *sql.DB) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		if err := checkSchema(ctx, db); err != nil {
			http.Error(w, "schema unavailable", http.StatusServiceUnavailable)
			return
		}
		respond(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /rows", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Marker string `json:"marker"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil || input.Marker == "" || len(input.Marker) > 1024 {
			http.Error(w, "marker required (1-1024 bytes)", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		row, err := writeRow(ctx, db, input.Marker)
		if err != nil {
			http.Error(w, "write refused", http.StatusServiceUnavailable)
			return
		}
		respond(w, http.StatusCreated, row)
	})
	mux.HandleFunc("GET /rows/count", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		var count int64
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM fixture_commits").Scan(&count); err != nil {
			http.Error(w, "read unavailable", http.StatusServiceUnavailable)
			return
		}
		respond(w, http.StatusOK, map[string]int64{"count": count})
	})
	mux.HandleFunc("GET /rows/latest", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		var row commitRow
		err := db.QueryRowContext(ctx, "SELECT sequence,marker,committed_at FROM fixture_commits ORDER BY sequence DESC LIMIT 1").Scan(&row.Sequence, &row.Marker, &row.CommittedAt)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "no rows", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, "read unavailable", http.StatusServiceUnavailable)
			return
		}
		respond(w, http.StatusOK, row)
	})
	return mux
}

func writeRow(ctx context.Context, db *sql.DB, marker string) (commitRow, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return commitRow{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err = verifySchema(ctx, tx); err != nil {
		return commitRow{}, err
	}
	row := commitRow{Marker: marker, CommittedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	result, err := tx.ExecContext(ctx, "INSERT INTO fixture_commits(marker,committed_at) VALUES(?,?)", row.Marker, row.CommittedAt)
	if err != nil {
		return commitRow{}, err
	}
	row.Sequence, err = result.LastInsertId()
	if err != nil {
		return commitRow{}, err
	}
	return row, tx.Commit()
}

func respond(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Print("response write failed")
	}
}

func run() error {
	path := os.Getenv("SQLITE_PATH")
	if path == "" {
		path = "/data/app.db"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := openDatabase(ctx, path)
	if err != nil {
		return fmt.Errorf("database startup refused: %w", err)
	}
	defer func() { _ = db.Close() }()
	server := &http.Server{Addr: ":8080", Handler: routes(db), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second}
	return server.ListenAndServe()
}

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
