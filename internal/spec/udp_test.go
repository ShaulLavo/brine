package spec

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestStrictUDPTable(t *testing.T) {
	base, err := os.ReadFile("testdata/valid-minimal.toml")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ table, code string }{
		{`[udp]
container_port=4433
exposure="public"`, ""},
		{`[udp]
container_port=4433
exposure="tailnet"`, ""},
		{`[udp]
container_port=4433`, "spec.required"},
		{`[udp]
exposure="public"`, "spec.required"},
		{`[udp]
container_port=80
exposure="public"`, "spec.invalid_port"},
		{`[udp]
container_port=65536
exposure="public"`, "spec.invalid_port"},
		{`[udp]
container_port="4433"
exposure="public"`, "spec.invalid_type"},
		{`[udp]
container_port=4433
exposure="Public"`, "spec.invalid_udp"},
		{`[udp]
container_port=4433
exposure="public"
host_port=4433`, "spec.unknown_field"},
		{`[[udp]]
container_port=4433
exposure="public"`, "spec.invalid_toml"},
		{`[UDP]
container_port=4433
exposure="public"`, "spec.unknown_field"},
	} {
		t.Run(strings.ReplaceAll(test.table, "\n", " "), func(t *testing.T) {
			app, err := Parse(append(append([]byte{}, base...), []byte("\n"+test.table)...))
			if test.code == "" {
				if err != nil || app.UDP == nil {
					t.Fatal(app.UDP, err)
				}
				return
			}
			var failure *Error
			if !errors.As(err, &failure) || failure.Code != test.code {
				t.Fatalf("want %s, got %v", test.code, err)
			}
		})
	}
}
