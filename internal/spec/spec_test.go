package spec

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestFixtures(t *testing.T) {
	cases := []struct{ name, code, field string }{
		{"zero-port", "spec.invalid_port", "container_port"},
		{"negative-port", "spec.invalid_port", "container_port"},
		{"name-uppercase", "spec.invalid_name", "name"},
		{"name-long", "spec.invalid_name", "name"},
		{"name-leading-hyphen", "spec.invalid_name", "name"},
		{"name-trailing-hyphen", "spec.invalid_name", "name"},
		{"domain-unicode-fold", "spec.invalid_domain", "domains[0]"},
		{"domain-unicode", "spec.invalid_domain", "domains[0]"},
		{"domain-trailing-dot", "spec.invalid_domain", "domains[0]"},
		{"domain-address", "spec.invalid_domain", "domains[0]"},
		{"domain-wildcard", "spec.invalid_domain", "domains[0]"},
		{"domain-empty", "spec.invalid_domain", "domains"},
		{"domain-duplicate", "spec.invalid_domain", "domains[1]"},
		{"domain-type", "spec.invalid_type", "domains[0]"},
		{"image-scheme", "spec.invalid_image", "image"},
		{"image-repository", "spec.invalid_image", "image"},
		{"image-implicit", "spec.invalid_image", "image"},
		{"image-credentials", "spec.invalid_image", "image"},
		{"image-algorithm", "spec.invalid_image", "image"},
		{"health-relative", "spec.invalid_health_path", "health.path"},
		{"health-query", "spec.invalid_health_path", "health.path"},
		{"health-escape", "spec.invalid_health_path", "health.path"},
		{"health-unclean", "spec.invalid_health_path", "health.path"},
		{"health-fragment", "spec.invalid_health_path", "health.path"},
		{"health-space", "spec.invalid_health_path", "health.path"},
		{"health-backslash", "spec.invalid_health_path", "health.path"},
		{"health-type", "spec.invalid_type", "health.expected_status"},
		{"health-deadline", "spec.invalid_health", "health.startup_deadline_seconds"},
		{"health-timeout-deadline", "spec.invalid_health", "health.timeout_seconds"},
		{"environment-type", "spec.invalid_type", "environment"},
		{"environment-nul", "spec.invalid_environment_value", "environment"},
		{"secret-key", "spec.invalid_environment_key", "secrets"},
		{"secret-type", "spec.invalid_type", "secrets"},
		{"duplicate-table", "spec.invalid_toml", "$"},
		{"duplicate-map-key", "spec.invalid_toml", "$"},
		{"dotted-duplicate", "spec.invalid_toml", "$"},
		{"resources-missing", "spec.required", "resources.memory_mb"},
		{"resources-pids", "spec.invalid_resources", "resources.pids_limit"},
		{"resources-overflow", "spec.invalid_resources", "resources.memory_mb"},
		{"environment-nested", "spec.invalid_type", "environment"},
		{"host-injection", "spec.unknown_field", "$.[unknown]"},
		{"health-table-shape", "spec.invalid_toml", "$"},
		{"case-alias", "spec.unknown_field", "$.[unknown]"},
		{"health-case-alias", "spec.unknown_field", "$.health.[unknown]"},
		{"empty-unknown", "spec.unknown_field", "$.[unknown]"},
		{"valid-minimal", "", ""}, {"valid-full", "", ""},
		{"unknown", "spec.unknown_field", "$.[unknown]"},
		{"duplicate", "spec.invalid_toml", "$"},
		{"schema", "spec.schema_version", "schema_version"},
		{"missing", "spec.required", "name"},
		{"wrong-type", "spec.invalid_type", "container_port"},
		{"name", "spec.invalid_name", "name"},
		{"domain", "spec.invalid_domain", "domains[0]"},
		{"idn", "spec.invalid_domain", "domains[0]"},
		{"unpinned", "spec.invalid_image", "image"},
		{"digest", "spec.invalid_image", "image"},
		{"privileged-port", "spec.invalid_port", "container_port"},
		{"port-range", "spec.invalid_port", "container_port"},
		{"health-path", "spec.invalid_health_path", "health.path"},
		{"environment-key", "spec.invalid_environment_key", "environment"},
		{"collision", "spec.environment_secret_collision", "environment"},
		{"expansion", "spec.environment_expansion", "environment"},
		{"secret-reference", "spec.invalid_secret_reference", "secrets"},
		{"host-port", "spec.unknown_field", "$.[unknown]"},
		{"quadlet", "spec.unknown_field", "$.[unknown]"},
		{"podman", "spec.unknown_field", "$.[unknown]"},
		{"proxy", "spec.unknown_field", "$.[unknown]"},
		{"hook", "spec.unknown_field", "$.[unknown]"},
		{"path-mount", "spec.unknown_field", "$.[unknown]"},
		{"nested-unknown", "spec.unknown_field", "$.health.[unknown]"},
		{"health-status", "spec.invalid_health", "health.expected_status"},
		{"health-timeout", "spec.invalid_health", "health.timeout_seconds"},
		{"resources", "spec.invalid_resources", "resources.memory_mb"},
		{"syntax", "spec.invalid_toml", "$"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", tc.name+".toml"))
			if err != nil {
				t.Fatal(err)
			}
			got, err := Parse(data)
			if tc.code != "" {
				var e *Error
				if !errors.As(err, &e) {
					t.Fatalf("expected typed error, got %v", err)
				}
				if e.Code != tc.code || e.Field != tc.field {
					t.Fatalf("error = %#v", e)
				}
				if !reflect.DeepEqual(got, App{}) {
					t.Fatal("invalid input returned an app")
				}
				if strings.Contains(err.Error(), "DO_NOT_ECHO") {
					t.Fatal("diagnostic leaked input")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := App{SchemaVersion: 1, Name: Name("hello"), Image: ImageReference("ghcr.io/example/hello@sha256:" + strings.Repeat("a", 64)), ContainerPort: Port(3000), Domains: []Domain{"hello.example.com"}, Health: Health{Path: "/", ExpectedStatus: 200, StartupDeadlineSeconds: 30, TimeoutSeconds: 3}, Environment: map[string]string{}, Secrets: map[string]SecretReference{}}
			if tc.name == "valid-full" {
				want.Health = Health{Path: "/healthz", ExpectedStatus: 204, StartupDeadlineSeconds: 45, TimeoutSeconds: 5}
				want.Resources = &Resources{MemoryMB: 256, PIDsLimit: 128}
				want.Environment["APP_ENV"] = "production"
				want.Secrets["DATABASE_KEY"] = "hello-db-key"
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %#v; want %#v", got, want)
			}
		})
	}
}

func FuzzParse(f *testing.F) {
	files, err := filepath.Glob("testdata/*.toml")
	if err != nil {
		f.Fatal(err)
	}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		app, err := Parse(data)
		if err != nil {
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("untyped error %T", err)
			}
			if !reflect.DeepEqual(app, App{}) {
				t.Fatal("partial app")
			}
		}
	})
}

func TestBoundaryValues(t *testing.T) {
	data, err := os.ReadFile("testdata/valid-minimal.toml")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ old, new string }{
		{"container_port = 3000", "container_port = 1024"},
		{"container_port = 3000", "container_port = 65535"},
		{"name = \"hello\"", "name = \"a\""},
		{"name = \"hello\"", "name = \"" + strings.Repeat("a", 63) + "\""},
		{"hello@sha256:", "hello:v1@sha256:"},
		{"ghcr.io/", "ghcr.io:5000/"},
		{strings.Repeat("a", 64), strings.Repeat("A", 64)},
	} {
		t.Run(tc.new, func(t *testing.T) {
			app, err := Parse([]byte(strings.ReplaceAll(string(data), tc.old, tc.new)))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(app.Image), strings.Repeat("A", 64)) {
				t.Fatal("digest was not normalized")
			}
		})
	}
}

func TestInputLimit(t *testing.T) {
	app, err := Parse(bytes.Repeat([]byte("x"), (1<<20)+1))
	var e *Error
	if !errors.As(err, &e) || e.Code != "spec.too_large" || e.Field != "$" || !reflect.DeepEqual(app, App{}) {
		t.Fatalf("unexpected limit result %v", err)
	}
}

func TestDeterministicMapRefusal(t *testing.T) {
	data, err := os.ReadFile("testdata/valid-minimal.toml")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, []byte("[environment]\nZ=42\nA=\"${DO_NOT_ECHO}\"\n")...)
	for i := 0; i < 100; i++ {
		_, err := Parse(data)
		var e *Error
		if !errors.As(err, &e) || e.Code != "spec.environment_expansion" {
			t.Fatalf("nondeterministic error %v", err)
		}
	}
}
