package policy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/ShaulLavo/brine/internal/spec"
)

func TestNormalizationIdempotenceAndPermutations(t *testing.T) {
	p, err := Parse(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	for seed := uint64(0); seed < 100; seed++ {
		a := app(t)
		a.Domains = nil
		for n := 0; n < 10; n++ {
			a.Domains = append(a.Domains, spec.Domain(fmt.Sprintf("app-%d.example.com", n)))
		}
		first, err := Normalize(a, p)
		if err != nil {
			t.Fatal(err)
		}
		want, err := first.CanonicalBytes()
		if err != nil {
			t.Fatal(err)
		}
		r := rand.New(rand.NewPCG(seed, seed+1))
		r.Shuffle(len(a.Domains), func(i, j int) { a.Domains[i], a.Domains[j] = a.Domains[j], a.Domains[i] })
		shuffled, err := Normalize(a, p)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := shuffled.CanonicalBytes()
		if !bytes.Equal(got, want) {
			t.Fatalf("seed %d changed canonical output", seed)
		}
		a.Domains = slices.Clone(first.Domains)
		a.Image = first.Image
		a.Health = spec.Health{Path: first.Health.Path, ExpectedStatus: first.Health.ExpectedStatus, StartupDeadlineSeconds: first.Health.StartupDeadlineSeconds, TimeoutSeconds: first.Health.TimeoutSeconds}
		a.Resources = &spec.Resources{MemoryMB: first.Resources.MemoryMB, PIDsLimit: first.Resources.PIDsLimit}
		a.Environment = map[string]string{}
		for _, e := range first.Environment {
			a.Environment[e.Name] = e.Value
		}
		a.Secrets = map[string]spec.SecretReference{}
		for _, s := range first.Secrets {
			a.Secrets[s.Name] = s.Reference
		}
		second, err := Normalize(a, p)
		if err != nil {
			t.Fatal(err)
		}
		got, _ = second.CanonicalBytes()
		if !bytes.Equal(want, got) {
			t.Fatal("normalization is not idempotent")
		}
	}
}

func FuzzDesiredCanonicalIdempotence(f *testing.F) {
	f.Add([]byte(`{"domains":["z.example.com","a.example.com"],"environment":[{"name":"B","value":"hidden"},{"name":"A","value":"hidden"}]}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 1<<16 {
			t.Skip()
		}
		var d Desired
		if json.Unmarshal(raw, &d) != nil {
			return
		}
		a, err := d.CanonicalBytes()
		if err != nil {
			t.Fatal(err)
		}
		var normalized Desired
		if err = json.Unmarshal(a, &normalized); err != nil {
			t.Fatal(err)
		}
		b, err := normalized.CanonicalBytes()
		if err != nil || !bytes.Equal(a, b) {
			t.Fatal("canonical output is not idempotent")
		}
		slices.Reverse(normalized.Domains)
		c, err := normalized.CanonicalBytes()
		if err != nil || !bytes.Equal(a, c) {
			t.Fatal("domain permutation changed output")
		}
	})
}
