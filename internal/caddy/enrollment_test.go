package caddy

import (
	"bytes"
	"testing"
)

func TestEnrollmentImportRestrictions(t *testing.T) {
	for _, input := range []string{"import other.caddy\n", "import /etc/caddy/brine/current/*.caddy\n", "{$ENV}\n", ":80 {\n import other.caddy\n}\n"} {
		if _, err := EnrollmentRoot([]byte(input)); err == nil {
			t.Fatalf("accepted %q", input)
		}
	}
	main := []byte(":80 {\n respond ok\n}\n")
	next, err := EnrollmentRoot(main)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(next, main) {
		t.Fatal("modified existing sites")
	}
}
