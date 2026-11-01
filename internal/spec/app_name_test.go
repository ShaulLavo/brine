package spec

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestAppNameAdmissionGrammar(t *testing.T) {
	base, err := os.ReadFile("testdata/valid-minimal.toml")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		valid bool
	}{
		{name: "a", valid: true},
		{name: "a0", valid: true},
		{name: "a-b", valid: true},
		{name: strings.Repeat("a", 62) + "0", valid: true},
		{name: "1"},
		{name: "1app"},
		{name: "-app"},
		{name: "app-"},
		{name: "App"},
		{name: ""},
		{name: strings.Repeat("a", 64)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			checkAppNameAdmission(t, string(base), tc.name, tc.valid)
		})
	}
}

func checkAppNameAdmission(t *testing.T, base, name string, valid bool) {
	t.Helper()
	input := strings.Replace(base, `name = "hello"`, `name = "`+name+`"`, 1)
	app, err := Parse([]byte(input))
	routingErr := ValidateRouting(Name(name), []Domain{"123.example.com"}, 1024)
	if valid {
		if err != nil || string(app.Name) != name || routingErr != nil {
			t.Fatalf("name %q: Parse = %#v, %v; ValidateRouting = %v", name, app, err, routingErr)
		}
		return
	}
	assertInvalidAppName(t, err)
	assertInvalidAppName(t, routingErr)
	if !reflect.DeepEqual(app, App{}) {
		t.Fatalf("Parse(%q) returned %#v", name, app)
	}
}

func assertInvalidAppName(t *testing.T, err error) {
	t.Helper()
	var refusal *Error
	if !errors.As(err, &refusal) || refusal.Code != "spec.invalid_name" || refusal.Field != "name" {
		t.Fatalf("expected spec.invalid_name at name, got %v", err)
	}
}

func TestAppNamePreservesNumericDNSLabels(t *testing.T) {
	base, err := os.ReadFile("testdata/valid-minimal.toml")
	if err != nil {
		t.Fatal(err)
	}
	input := strings.NewReplacer("hello.example.com", "123.example.com", "ghcr.io", "123.registry.example.com").Replace(string(base))
	app, err := Parse([]byte(input))
	if err != nil {
		t.Fatalf("numeric DNS or registry label refused: %v", err)
	}
	if len(app.Domains) != 1 || app.Domains[0] != "123.example.com" || !strings.HasPrefix(string(app.Image), "123.registry.example.com/") {
		t.Fatalf("numeric DNS or registry label changed: %#v", app)
	}
}
