package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRules(t *testing.T) {
	for _, tc := range []struct {
		name, file, source, rule string
	}{
		{"cobra domain", "internal/plan/a.go", `package p; import "github.com/spf13/cobra"`, "presentation-import"},
		{"charm domain", "internal/plan/a.go", `package p; import "charm.land/bubbletea/v2"`, "presentation-import"},
		{"charm old module", "internal/plan/a.go", `package p; import "github.com/charmbracelet/lipgloss"`, "presentation-import"},
		{"ui allowed", "internal/ui/a.go", `package p; import "charm.land/lipgloss/v2"`, ""},
		{"cli allowed", "internal/cli/a.go", `package p; import "github.com/spf13/cobra"`, ""},
		{"cmd allowed", "cmd/brine/a.go", `package p; import "github.com/spf13/cobra"`, ""},
		{"exec alias", "internal/plan/a.go", `package p; import e "os/exec"`, "exec-import"},
		{"exec use with old import", "internal/plan/a.go", `package p; import e "os/exec"; func f(){ e.Command("sh") }`, "exec-use"},
		{"exec dot", "internal/plan/a.go", `package p; import . "os/exec"`, "exec-import"},
		{"exec runner", "internal/localexec/a.go", `package p; import "os/exec"`, ""},
		{"exec tests", "internal/plan/a_test.go", `package p; import "os/exec"`, ""},
		{"rand", "internal/plan/a.go", `package p; import "math/rand"`, "weak-random"},
		{"rand v2", "internal/plan/a.go", `package p; import "math/rand/v2"`, "weak-random"},
		{"TLS literal", "internal/plan/a.go", `package p; var x = struct{ InsecureSkipVerify bool }{InsecureSkipVerify:true}`, "insecure-tls"},
		{"TLS assignment", "internal/plan/a.go", `package p; func f(){ c.InsecureSkipVerify = true }`, "insecure-tls"},
		{"sleep alias", "internal/plan/a.go", `package p; import clock "time"; func f(){ clock.Sleep(1) }`, "blocking-sleep"},
		{"sleep value", "internal/plan/a.go", `package p; import "time"; var sleeper = time.Sleep`, "blocking-sleep"},
		{"sleep dot", "internal/plan/a.go", `package p; import . "time"; func f(){ Sleep(1) }`, "dot-import"},
		{"sleep test", "internal/plan/a_test.go", `package p; import "time"; func f(){ time.Sleep(1) }`, ""},
		{"shadowed package", "internal/plan/a.go", `package p; import "time"; func f(){ time := custom{}; time.Sleep(1) }`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := scanFile(tc.file, []byte(tc.source))
			if err != nil {
				t.Fatal(err)
			}
			if tc.rule == "" {
				if len(got) != 0 {
					t.Fatalf("unexpected findings: %v", got)
				}
				return
			}
			for _, f := range got {
				if strings.HasPrefix(f.Key, tc.rule+"|") {
					return
				}
			}
			t.Fatalf("want %s, got %v", tc.rule, got)
		})
	}
}

func TestRatchetCountsAndMissingBaseline(t *testing.T) {
	findings := []finding{{Key: "old"}, {Key: "old"}, {Key: "new"}}
	got := additions(findings, map[string]int{"old": 1})
	if len(got) != 2 || got[0].Key != "old" || got[1].Key != "new" {
		t.Fatalf("ratchet permitted another copy: %v", got)
	}
	if _, err := readBaseline(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("missing baseline must fail closed")
	}
}

func TestRuleKeysIgnoreLineOffsetsButNotNewFunctions(t *testing.T) {
	source := `package p; import "time"; func f(){ time.Sleep(1) }`
	a, err := scanFile("internal/plan/a.go", []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	b, err := scanFile("internal/plan/a.go", []byte("\n\n"+source))
	if err != nil {
		t.Fatal(err)
	}
	if a[0].Key != b[0].Key {
		t.Fatal("moving a finding changed the baseline key")
	}
	c, err := scanFile("internal/plan/a.go", []byte(strings.ReplaceAll(source, "func f", "func g")))
	if err != nil {
		t.Fatal(err)
	}
	if a[0].Key == c[0].Key {
		t.Fatal("copying a finding into a new function reused the exemption")
	}
}

func TestDuplicatesRatchetUsesContentNotLines(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "a.go")
	if err := os.WriteFile(path, []byte("package p\nfunc a(){ println(1) }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	input := []byte(`{"Issues":[{"FromLinter":"dupl","Text":"2-2 lines are duplicate of ` + "`b.go:3-3`" + `","Pos":{"Filename":"a.go","Line":2},"LineRange":{"From":2,"To":2}}]}`)
	first, err := duplicateFindings(root, input)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("package p\n\nfunc a(){ println(1) }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	shifted := strings.ReplaceAll(strings.ReplaceAll(string(input), "2-2", "3-3"), `"Line":2`, `"Line":3`)
	shifted = strings.ReplaceAll(strings.ReplaceAll(shifted, `"From":2`, `"From":3`), `"To":2`, `"To":3`)
	second, err := duplicateFindings(root, []byte(shifted))
	if err != nil {
		t.Fatal(err)
	}
	if first[0].Key != second[0].Key {
		t.Fatal("line movement should not require rebaselining")
	}
	if err := os.WriteFile(path, []byte("package p\n\nfunc a(){ println(2) }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	changed, err := duplicateFindings(root, []byte(shifted))
	if err != nil {
		t.Fatal(err)
	}
	if len(additions(changed, map[string]int{first[0].Key: 1})) != 1 {
		t.Fatal("changed clone escaped the gate")
	}
	if _, err := duplicateFindings(root, []byte(`{}`)); err == nil {
		t.Fatal("missing Issues is not a clean report")
	}
}

func TestDeadcodeUsesQualifiedSymbols(t *testing.T) {
	input := []byte(`[{"Path":"example/p","Funcs":[{"Name":"T.F","Position":{"File":"p/f.go","Line":12}}]}]`)
	got, err := deadcodeFindings(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Key != "example/p.T.F" {
		t.Fatalf("unexpected symbols: %v", got)
	}
	var allow map[string]string
	if err := json.Unmarshal([]byte(`{"example/p.T.F":"test adapter"}`), &allow); err != nil {
		t.Fatal(err)
	}
	if len(filterAllowed(got, allow)) != 0 {
		t.Fatal("deliberate entry point was not exempted")
	}
}

func TestRuleWalkIncludesOtherOSAndIgnoresScratch(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "internal", "example"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".tmp"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "internal", "example", "random_windows.go"), []byte("//go:build windows\n\npackage example\nimport \"math/rand\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".tmp", "invalid.go"), []byte("not Go"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := ruleFindings(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !strings.HasPrefix(got[0].Key, "weak-random|") {
		t.Fatalf("platform-specific production file escaped checks: %v", got)
	}
}

func TestRunRejectsNewRuleAndAcceptsExplicitBaseline(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "unsafe.go")
	if err := os.WriteFile(file, []byte("package example\nimport \"math/rand\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	baseline := filepath.Join(t.TempDir(), "rules.json")
	if err := os.WriteFile(baseline, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"rules", "--root", root, "--baseline", baseline}
	if err := run(args); err == nil {
		t.Fatal("a newly introduced rule violation must fail the CLI")
	}
	if err := run(append(args, "--write-baseline")); err != nil {
		t.Fatal(err)
	}
	if err := run(args); err != nil {
		t.Fatal(err)
	}
}

func TestDeadcodeFailsClosedOnIncompleteRecords(t *testing.T) {
	for _, input := range []string{"null", "{}", "[{}]", `[{"Path":"p"}]`, `[{"Path":"p","Funcs":[{"Name":"F"}]}]`} {
		if _, err := deadcodeFindings([]byte(input)); err == nil {
			t.Fatalf("incomplete report accepted: %s", input)
		}
	}
	if got, err := deadcodeFindings([]byte("[]")); err != nil || len(got) != 0 {
		t.Fatalf("empty valid report rejected: %v, %v", got, err)
	}
}
