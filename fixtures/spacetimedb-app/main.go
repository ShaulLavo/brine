package main

import (
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const schemaMarker = "brine-recovery-v1"

//go:embed web/*
var assets embed.FS

type publicConfig struct {
	Endpoint string `json:"endpoint"`
	Database string `json:"database"`
}

func loadConfig() (publicConfig, error) {
	if os.Getenv("BRINE_DB_MAIN_ENGINE") != "spacetimedb" {
		return publicConfig{}, errors.New("BRINE_DB_MAIN_ENGINE must be spacetimedb")
	}
	if os.Getenv("BRINE_DB_MAIN_ACCEPTS") != schemaMarker {
		return publicConfig{}, errors.New("BRINE_DB_MAIN_ACCEPTS must match the compiled fixture marker")
	}
	endpoint := os.Getenv("BRINE_DB_MAIN_URL")
	if len(endpoint) > 4096 {
		return publicConfig{}, errors.New("BRINE_DB_MAIN_URL exceeds the fixture URL bound")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return publicConfig{}, errors.New("BRINE_DB_MAIN_URL requires an HTTP or HTTPS SDK base")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || !strings.HasSuffix(parsed.Path, "/") {
		return publicConfig{}, errors.New("BRINE_DB_MAIN_URL requires a trailing slash and no credentials, query or fragment")
	}
	name := os.Getenv("BRINE_DB_MAIN_NAME")
	if name == "" || len(name) > 128 || strings.ContainsAny(name, "/?#\\\t\r\n ") || name == "." || name == ".." {
		return publicConfig{}, errors.New("BRINE_DB_MAIN_NAME requires one bounded database name")
	}
	return publicConfig{Endpoint: endpoint, Database: name}, nil
}

func routes(config publicConfig) (http.Handler, error) {
	files, err := fs.Sub(assets, "web")
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{\"ok\":true}\n"))
	})
	mux.HandleFunc("GET /config.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(body)
	})
	mux.Handle("GET /", http.FileServer(http.FS(files)))
	return mux, nil
}

func main() {
	config, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}
	handler, err := routes(config)
	if err != nil {
		log.Fatal(err)
	}
	listen := os.Getenv("BRINE_FIXTURE_LISTEN")
	if listen == "" {
		listen = ":8080"
	}
	server := &http.Server{Addr: listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
	log.Fatal(server.ListenAndServe())
}
