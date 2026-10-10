//go:build linux

package host

import (
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/store"
)

func TestCredentialDatabaseSelection(t *testing.T) {
	scopes := []store.CredentialScope{{Database: data.DatabaseBinding{Name: "primary"}}, {Database: data.DatabaseBinding{Name: "audit"}}}
	if _, err := selectCredentialScope(scopes, ""); err == nil {
		t.Fatal("ambiguous database selected")
	}
	selected, err := selectCredentialScope(scopes, "audit")
	if err != nil || selected.Database.Name != "audit" {
		t.Fatal("explicit database not selected", err)
	}
	if _, err := selectCredentialScope(scopes, "missing"); err == nil {
		t.Fatal("unknown database selected")
	}
	selected, err = selectCredentialScope(scopes[:1], "")
	if err != nil || selected.Database.Name != "primary" {
		t.Fatal("single database refused", err)
	}
	scopes[1].Database.Name = "primary"
	if _, err := selectCredentialScope(scopes, "primary"); err == nil {
		t.Fatal("duplicate database accepted")
	}
}
