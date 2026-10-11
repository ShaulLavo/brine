package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func configure(t *testing.T) {
	t.Helper()
	t.Setenv("BRINE_DB_MAIN_ENGINE", "spacetimedb")
	t.Setenv("BRINE_DB_MAIN_URL", "https://fixture.example/_brine/db/main/")
	t.Setenv("BRINE_DB_MAIN_NAME", "recovery-fixture")
	t.Setenv("BRINE_DB_MAIN_ACCEPTS", schemaMarker)
}

func TestPublicConfigAndAssets(t *testing.T) {
	configure(t)
	t.Setenv("BRINE_DB_MAIN_PUBLISHER_TOKEN", "must-stay-private")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "must-stay-private")
	config, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	handler, err := routes(config)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	response, err := server.Client().Get(server.URL + "/config.json")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]string
	if err = json.Unmarshal(body, &values); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || len(values) != 2 || values["endpoint"] != config.Endpoint || values["database"] != config.Database || strings.Contains(string(body), "must-stay-private") {
		t.Fatalf("public configuration escaped its two-field contract: %q", body)
	}
	for path, status := range map[string]int{"/": 200, "/health": 200, "/module-v1.wasm": 404, "/client/package.json": 404} {
		assertRoute(t, handler, path, status)
	}
}

func assertRoute(t *testing.T, handler http.Handler, path string, status int) {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
	if response.Code != status {
		t.Errorf("%s returned %d, want %d", path, response.Code, status)
	}
}

func TestInvalidManagedEnvironment(t *testing.T) {
	cases := map[string]string{
		"BRINE_DB_MAIN_ENGINE":  "sqlite",
		"BRINE_DB_MAIN_ACCEPTS": "unknown-marker",
		"BRINE_DB_MAIN_URL":     "https://user:password@fixture.example/",
		"BRINE_DB_MAIN_NAME":    "../other",
	}
	for field, value := range cases {
		t.Run(field, func(t *testing.T) {
			assertInvalidConfig(t, field, value)
		})
	}
	t.Run("legacy variables do not substitute", func(t *testing.T) {
		configure(t)
		t.Setenv("BRINE_DB_MAIN_URL", "")
		t.Setenv("SPACETIMEDB_ENDPOINT", "https://fixture.example/")
		if _, err := loadConfig(); err == nil {
			t.Fatal("unmanaged endpoint substituted for managed environment")
		}
	})
}

func assertInvalidConfig(t *testing.T, field string, value string) {
	t.Helper()
	configure(t)
	t.Setenv(field, value)
	if _, err := loadConfig(); err == nil {
		t.Fatal("unsafe fixture environment was accepted")
	}
}
