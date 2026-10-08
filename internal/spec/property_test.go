package spec

import (
	"encoding/hex"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"testing/quick"
)

func generatedSpec(n uint64, sum [32]byte) []byte {
	return []byte(fmt.Sprintf("schema_version=1\nname=\"app-%x\"\nimage=\"registry.example.com/team/app@sha256:%s\"\ncontainer_port=3000\ndomains=[\"APP-%X.EXAMPLE.COM\"]\n[health]\npath=\"/ready/%x\"\n", n, strings.ToUpper(hex.EncodeToString(sum[:])), n, n))
}

func checkGeneratedSpec(t *testing.T, n uint64, sum [32]byte) {
	t.Helper()
	a, err := Parse(generatedSpec(n, sum))
	if err != nil {
		t.Fatal(err)
	}
	image, ok := imageReference(string(a.Image))
	if !ok || image != string(a.Image) {
		t.Fatal("image normalization is not idempotent")
	}
	if !validDomain(string(a.Domains[0])) || string(a.Domains[0]) != strings.ToLower(string(a.Domains[0])) || !validHealthPath(string(a.Health.Path)) {
		t.Fatal("generated domain/path invalid")
	}
	b, err := Parse([]byte(fmt.Sprintf("schema_version=1\nname=%q\nimage=%q\ncontainer_port=3000\ndomains=[%q]\n[health]\npath=%q\n", a.Name, a.Image, a.Domains[0], a.Health.Path)))
	if err != nil || !reflect.DeepEqual(a, b) {
		t.Fatalf("normalized parse differs: %v", err)
	}
}

func TestGeneratedValidSpec(t *testing.T) {
	err := quick.Check(func(n uint64, sum [32]byte) bool { checkGeneratedSpec(t, n, sum); return true }, &quick.Config{MaxCount: 500})
	if err != nil {
		t.Fatal(err)
	}
}

func FuzzGeneratedValidSpec(f *testing.F) {
	f.Add(uint64(0), []byte("digest"))
	f.Add(^uint64(0), []byte{})
	f.Fuzz(func(t *testing.T, n uint64, b []byte) {
		var sum [32]byte
		copy(sum[:], b)
		checkGeneratedSpec(t, n, sum)
	})
}

func FuzzHealthPath(f *testing.F) {
	for _, p := range []string{"/", "/ready", "/../escape", "//other.example.com", "/a%2fb", "/a?token=SYNTHETIC_PRIVATE"} {
		f.Add(p)
	}
	f.Fuzz(func(t *testing.T, p string) {
		if len(p) > 1024 {
			t.Skip()
		}
		input := fmt.Sprintf("schema_version=1\nname=\"app\"\nimage=\"registry.example.com/team/app@sha256:%s\"\ncontainer_port=3000\ndomains=[\"app.example.com\"]\n[health]\npath=%q\n", strings.Repeat("a", 64), p)
		a, err := Parse([]byte(input))
		if err == nil && (!validHealthPath(p) || string(a.Health.Path) != p) {
			t.Fatal("unsafe or changed path accepted")
		}
		if err != nil && strings.Contains(err.Error(), "SYNTHETIC_PRIVATE") {
			t.Fatal("error leaked input")
		}
	})
}
