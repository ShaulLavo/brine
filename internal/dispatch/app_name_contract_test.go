package dispatch

import (
	"os"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/spec"
)

func TestAppNameBoundaryAgreement(t *testing.T) {
	base, err := os.ReadFile("../spec/testdata/valid-minimal.toml")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a", "a0", "a-b", strings.Repeat("a", 62) + "0"} {
		t.Run(name, func(t *testing.T) {
			checkAppNameBoundaryAgreement(t, base, name)
		})
	}
}

func checkAppNameBoundaryAgreement(t *testing.T, base []byte, name string) {
	t.Helper()
	input := strings.Replace(string(base), `name = "hello"`, `name = "`+name+`"`, 1)
	app, err := spec.Parse([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if !ValidApp(string(app.Name)) {
		t.Fatalf("dispatch rejected parsed app name %q", app.Name)
	}
	if !ops.ValidIntent(ops.Intent{Kind: ops.SecretSet, App: string(app.Name), SecretRef: "ref"}) {
		t.Fatalf("operation grammar rejected parsed app name %q", app.Name)
	}
}
