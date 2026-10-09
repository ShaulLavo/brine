package dispatch

import (
	"os"
	"testing"
)

func mustFixture(t *testing.T) []byte {
	t.Helper()
	b, e := os.ReadFile("../target/testdata/ready-arm64.json")
	if e != nil {
		t.Fatal(e)
	}
	return b
}
