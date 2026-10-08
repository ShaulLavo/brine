package plan

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spec"
)

func TestGeneratedOrderingAndHashedFields(t *testing.T) {
	for seed := uint64(0); seed < 100; seed++ {
		in := fixture(t, "ready-arm64")
		for n := 0; n < 10; n++ {
			in.Desired.Domains = append(in.Desired.Domains, spec.Domain(fmt.Sprintf("app-%d.example.com", n)))
			in.Desired.Environment = append(in.Desired.Environment, policy.Environment{Name: fmt.Sprintf("KEY_%d", n), Value: fmt.Sprintf("value-%d", seed)})
		}
		p := build(t, in)
		r := rand.New(rand.NewPCG(seed, seed+1))
		r.Shuffle(len(in.Desired.Domains), func(i, j int) {
			in.Desired.Domains[i], in.Desired.Domains[j] = in.Desired.Domains[j], in.Desired.Domains[i]
		})
		r.Shuffle(len(in.Desired.Environment), func(i, j int) {
			in.Desired.Environment[i], in.Desired.Environment[j] = in.Desired.Environment[j], in.Desired.Environment[i]
		})
		q := build(t, in)
		if p.Hash != q.Hash || !reflect.DeepEqual(p, q) {
			t.Fatalf("seed %d permutation changed plan", seed)
		}
		in.Desired.Environment[0].Value += "-changed"
		if build(t, in).Hash == p.Hash {
			t.Fatal("changed hashed environment retained hash")
		}
	}
	for _, test := range []struct {
		name   string
		change func(*Input)
	}{
		{"name", func(i *Input) { i.Desired.Name = "other" }},
		{"domain", func(i *Input) { i.Desired.Domains = []spec.Domain{"other.example.com"} }},
		{"image", func(i *Input) {
			i.Desired.Image = spec.ImageReference(strings.ReplaceAll(string(i.Desired.Image), strings.Repeat("a", 64), strings.Repeat("b", 64)))
			i.Image.Digest = "sha256:" + strings.Repeat("b", 64)
		}},
		{"health path", func(i *Input) { i.Desired.Health.Path = "/ready" }},
		{"container port", func(i *Input) { i.Desired.ContainerPort++ }},
		{"resources", func(i *Input) { i.Desired.Resources.MemoryMB++ }},
		{"port range", func(i *Input) { i.Desired.AppPorts.Max++ }},
	} {
		t.Run(test.name, func(t *testing.T) {
			in := fixture(t, "ready-arm64")
			p := build(t, in)
			test.change(&in)
			if build(t, in).Hash == p.Hash {
				t.Fatal("changed hashed field retained hash")
			}
		})
	}
}
