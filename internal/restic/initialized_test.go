package restic

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestInitializedRepositoryCannotComeFromSyntax(t *testing.T) {
	var zero InitializedRepository
	if zero.AdmitsCreation(testScope(), strings.Repeat("a", 64)) {
		t.Fatal("zero initialization proof admitted repository creation")
	}
	if _, err := json.Marshal(zero); err == nil {
		t.Fatal("serialized initialization proof could replace actual authenticated readback")
	}
}
