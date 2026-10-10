package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
)

func fixtureDatabase(t *testing.T, initialize bool) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func(db *sql.DB) { _ = db.Close() }(db)
	if initialize {
		raw, err := os.ReadFile("schema.json")
		if err != nil {
			t.Fatal(err)
		}
		var artifact struct {
			Definition data.SchemaDefinition `json:"definition"`
			Statements []string              `json:"statements"`
		}
		if err := json.Unmarshal(raw, &artifact); err != nil {
			t.Fatal(err)
		}
		for _, statement := range artifact.Statements {
			if _, err := db.Exec(statement); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := db.Exec(data.MarkerTableSQL); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("INSERT INTO brine_schema_marker VALUES(1,?,?)", artifact.Definition.Marker, artifact.Definition.CatalogSHA256); err != nil {
			t.Fatal(err)
		}
	} else {
		if _, err := db.Exec("PRAGMA user_version=0"); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestRefuseUninitialized(t *testing.T) {
	for _, path := range []string{fixtureDatabase(t, false), filepath.Join(t.TempDir(), "absent.db")} {
		db, err := openDatabase(context.Background(), path)
		if err == nil {
			_ = db.Close()
			t.Fatal("accepted missing schema")
		}
	}
}

func TestRefuseMarkerAndCatalogDrift(t *testing.T) {
	for _, statement := range []string{
		"DROP TABLE brine_schema_marker",
		"UPDATE brine_schema_marker SET marker='other'",
		"UPDATE brine_schema_marker SET catalog_sha256='wrong'",
		"CREATE TABLE unexpected(x TEXT)",
	} {
		t.Run(statement, func(t *testing.T) {
			path := fixtureDatabase(t, true)
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(statement); err != nil {
				t.Fatal(err)
			}
			if err = db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err = openDatabase(context.Background(), path)
			if err == nil {
				_ = db.Close()
				t.Fatal("accepted incompatible schema")
			}
		})
	}
}

func TestReviewedSQLMatchesInitializer(t *testing.T) {
	raw, err := os.ReadFile("schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	artifactRaw, err := os.ReadFile("schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var artifact struct {
		Statements []string `json:"statements"`
	}
	if err = json.Unmarshal(artifactRaw, &artifact); err != nil {
		t.Fatal(err)
	}
	if len(artifact.Statements) != 1 || string(raw) != artifact.Statements[0]+";\n" {
		t.Fatal("reviewed SQL and initializer diverged")
	}
}

func TestWriteCountLatestAndPreserve(t *testing.T) {
	path := fixtureDatabase(t, true)
	db, err := openDatabase(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func(db *sql.DB) { _ = db.Close() }(db)
	var mode string
	var timeout int
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("WAL: %s %v", mode, err)
	}
	if err := db.QueryRow("PRAGMA busy_timeout").Scan(&timeout); err != nil || timeout != 5000 {
		t.Fatalf("timeout: %d %v", timeout, err)
	}
	handler := routes(db)
	for _, tc := range []struct {
		method, path, body, expected string
		status                       int
	}{
		{"GET", "/health", "", `"ok":true`, 200},
		{"GET", "/rows/latest", "", "", 404},
		{"POST", "/rows", `{"marker":"sentinel-1"}`, `"sequence":1`, 201},
		{"GET", "/rows/count", "", `"count":1`, 200},
		{"GET", "/rows/latest", "", `"marker":"sentinel-1"`, 200},
		{"POST", "/rows", `{"marker":""}`, "", 400},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
		if response.Code != tc.status || !strings.Contains(response.Body.String(), tc.expected) {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, response.Code, response.Body.String())
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = openDatabase(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func(db *sql.DB) { _ = db.Close() }(db)
	var count int
	if err := db.QueryRow("SELECT count(*) FROM fixture_commits").Scan(&count); err != nil || count != 1 {
		t.Fatalf("restart lost row: %d %v", count, err)
	}
	if _, err := db.Exec("UPDATE brine_schema_marker SET marker='wrong'"); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	routes(db).ServeHTTP(response, httptest.NewRequest("POST", "/rows", strings.NewReader(`{"marker":"must-not-write"}`)))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("write after marker drift: %d", response.Code)
	}
	if err := db.QueryRow("SELECT count(*) FROM fixture_commits").Scan(&count); err != nil || count != 1 {
		t.Fatalf("drift wrote row: %d %v", count, err)
	}
}
