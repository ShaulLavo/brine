// Command checkgate ratchets architecture, reachability and clone findings.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/scanner"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type finding struct {
	Key, File, Detail string
	Line              int
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: checkgate rules|deadcode|duplicates [flags]")
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	root := flags.String("root", ".", "source root")
	input := flags.String("input", "", "tool JSON report")
	baseline := flags.String("baseline", "", "reviewed finding counts")
	allow := flags.String("allow", "", "deliberate unreachable symbols and reasons")
	write := flags.Bool("write-baseline", false, "explicitly replace the baseline for review")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *baseline == "" || flags.NArg() != 0 {
		return errors.New("a baseline path and no positional arguments are required")
	}
	var findings []finding
	var err error
	if args[0] == "rules" {
		findings, err = ruleFindings(*root)
	} else {
		data, readErr := os.ReadFile(*input) // #nosec G703 -- Explicit operator-selected tool report, not a host request.
		if readErr != nil {
			return readErr
		}
		switch args[0] {
		case "deadcode":
			findings, err = deadcodeFindings(data)
		case "duplicates":
			findings, err = duplicateFindings(*root, data)
		default:
			return fmt.Errorf("unknown check %q", args[0])
		}
	}
	if err != nil {
		return err
	}
	if *allow != "" {
		data, readErr := os.ReadFile(*allow) // #nosec G703 -- Explicit operator-selected reviewed allowlist.
		if readErr != nil {
			return readErr
		}
		var symbols map[string]string
		if err := json.Unmarshal(data, &symbols); err != nil {
			return err
		}
		if symbols == nil {
			return errors.New("allowlist must be an object")
		}
		for symbol, reason := range symbols {
			if symbol == "" || strings.TrimSpace(reason) == "" {
				return errors.New("every allowed symbol needs a reason")
			}
		}
		findings = filterAllowed(findings, symbols)
	}
	if *write {
		counts := make(map[string]int)
		for _, f := range findings {
			counts[f.Key]++
		}
		data, marshalErr := json.MarshalIndent(counts, "", "  ")
		if marshalErr != nil {
			return marshalErr
		}
		return os.WriteFile(*baseline, append(data, '\n'), 0600) // #nosec G703 -- Only the explicit baseline-maintenance flag writes this operator-selected file.
	}
	counts, err := readBaseline(*baseline)
	if err != nil {
		return err
	}
	newFindings := additions(findings, counts)
	for _, f := range newFindings {
		fmt.Printf("%s:%d: %s [%s]\n", f.File, f.Line, f.Detail, f.Key)
	}
	fmt.Printf("%s: %d findings, %d baseline, %d new\n", args[0], len(findings), len(findings)-len(newFindings), len(newFindings))
	if len(newFindings) != 0 {
		return errors.New("new findings require a fix, not automatic rebaselining")
	}
	return nil
}

func readBaseline(path string) (map[string]int, error) {
	data, err := os.ReadFile(path) // #nosec G304 G703 -- Explicit operator-selected baseline file.
	if err != nil {
		return nil, err
	}
	var counts map[string]int
	if err := json.Unmarshal(data, &counts); err != nil {
		return nil, err
	}
	if counts == nil {
		return nil, errors.New("baseline must be an object")
	}
	for _, count := range counts {
		if count <= 0 {
			return nil, errors.New("baseline counts must be positive")
		}
	}
	return counts, nil
}

func additions(findings []finding, baseline map[string]int) []finding {
	seen := make(map[string]int)
	var added []finding
	for _, f := range findings {
		seen[f.Key]++
		if seen[f.Key] > baseline[f.Key] {
			added = append(added, f)
		}
	}
	return added
}

func filterAllowed(findings []finding, allow map[string]string) []finding {
	var kept []finding
	for _, f := range findings {
		if _, ok := allow[f.Key]; !ok {
			kept = append(kept, f)
		}
	}
	return kept
}

func ruleFindings(root string) ([]finding, error) {
	tree, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tree.Close() }()
	var findings []finding
	// #nosec G703 -- Scanning the operator-selected checkout is the purpose of this local tool.
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != root && (strings.HasPrefix(entry.Name(), ".") || entry.Name() == "vendor" || entry.Name() == "dist" || entry.Name() == "bin") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		name, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		data, err := tree.ReadFile(name)
		if err != nil {
			return err
		}
		found, err := scanFile(filepath.ToSlash(name), data)
		findings = append(findings, found...)
		return err
	})
	return findings, err
}

func scanFile(name string, data []byte) ([]finding, error) {
	set := token.NewFileSet()
	file, err := parser.ParseFile(set, name, data, 0)
	if err != nil {
		return nil, err
	}
	var findings []finding
	add := func(rule, owner, detail string, node ast.Node) {
		var source bytes.Buffer
		if err := format.Node(&source, set, node); err != nil {
			panic(err) // Every node comes from a successfully parsed file.
		}
		key := fmt.Sprintf("%s|%s|%s|%x", rule, name, owner, sha256.Sum256(source.Bytes()))
		findings = append(findings, finding{Key: key, File: name, Line: set.Position(node.Pos()).Line, Detail: detail})
	}
	isTest := strings.HasSuffix(name, "_test.go")
	presentation := strings.HasPrefix(name, "internal/cli/") || strings.HasPrefix(name, "internal/ui/") || strings.HasPrefix(name, "cmd/")
	imports := make(map[string]string)
	for _, imp := range file.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			return nil, err
		}
		alias := filepath.Base(path)
		if imp.Name != nil {
			alias = imp.Name.Name
		}
		imports[alias] = path
		if alias == "." && !isTest {
			add("dot-import", "import", "dot imports hide package authority", imp)
		}
		if !presentation && (path == "github.com/spf13/cobra" || strings.HasPrefix(path, "charm.land/") || strings.HasPrefix(path, "github.com/charmbracelet/")) {
			add("presentation-import", "import", "domain packages must not import Cobra or Charm", imp)
		}
		if path == "os/exec" && !isTest && !strings.HasPrefix(name, "internal/localexec/") {
			add("exec-import", "import", "subprocess authority belongs in internal/localexec", imp)
		}
		if !isTest && (path == "math/rand" || path == "math/rand/v2") {
			add("weak-random", "import", "production randomness must use crypto/rand", imp)
		}
	}
	for _, decl := range file.Decls {
		owner := "package"
		if fn, ok := decl.(*ast.FuncDecl); ok {
			owner = fn.Name.Name
			if fn.Recv != nil {
				var receiver bytes.Buffer
				if err := format.Node(&receiver, set, fn.Recv.List[0].Type); err != nil {
					return nil, err
				}
				owner = receiver.String() + "." + owner
			}
		}
		ast.Inspect(decl, func(node ast.Node) bool {
			if id, ok := node.(*ast.Ident); ok && id.Name == "InsecureSkipVerify" {
				add("insecure-tls", owner, "TLS verification must never be disabled", id)
			}
			if sel, ok := node.(*ast.SelectorExpr); ok && !isTest && !strings.HasPrefix(name, "internal/localexec/") {
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Obj == nil && imports[pkg.Name] == "os/exec" {
					add("exec-use", owner, "subprocess APIs must stay in internal/localexec", sel)
				}
			}
			if sel, ok := node.(*ast.SelectorExpr); ok && sel.Sel.Name == "Sleep" && !isTest {
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Obj == nil && imports[pkg.Name] == "time" {
					// No production helper needs Sleep today. Future poll helpers need a
					// reviewed exact-function exemption, not a package-wide bypass.
					add("blocking-sleep", owner, "use a context-aware timer for polling or retries", sel)
				}
			}
			return true
		})
	}
	return findings, nil
}

type deadPackage struct {
	Path  string
	Funcs []struct {
		Name     string
		Position struct {
			File string
			Line int
		}
	}
}

func deadcodeFindings(data []byte) ([]finding, error) {
	var packages []deadPackage
	if err := json.Unmarshal(data, &packages); err != nil {
		return nil, err
	}
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return nil, errors.New("deadcode report must be an array")
	}
	var findings []finding
	for _, pkg := range packages {
		if pkg.Path == "" || pkg.Funcs == nil {
			return nil, errors.New("incomplete deadcode package")
		}
		for _, fn := range pkg.Funcs {
			if pkg.Path == "" || fn.Name == "" || fn.Position.File == "" || fn.Position.Line <= 0 {
				return nil, errors.New("incomplete deadcode finding")
			}
			findings = append(findings, finding{Key: pkg.Path + "." + fn.Name, File: fn.Position.File, Line: fn.Position.Line, Detail: "unreachable " + fn.Name})
		}
	}
	return findings, nil
}

var clonePeer = regexp.MustCompile("duplicate of `([^`]+):[0-9]+-[0-9]+`")

func duplicateFindings(root string, data []byte) ([]finding, error) {
	tree, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tree.Close() }()
	var report struct {
		Issues *[]struct {
			FromLinter, Text string
			Pos              struct {
				Filename string
				Line     int
			}
			LineRange struct{ From, To int }
		}
	}
	if err := json.Unmarshal(data, &report); err != nil {
		return nil, err
	}
	if report.Issues == nil {
		return nil, errors.New("golangci report must contain Issues")
	}
	var findings []finding
	for _, issue := range *report.Issues {
		if issue.FromLinter != "dupl" {
			return nil, fmt.Errorf("unexpected %s issue in clone report", issue.FromLinter)
		}
		peer := clonePeer.FindStringSubmatch(issue.Text)
		if len(peer) != 2 {
			return nil, fmt.Errorf("unrecognized clone report: %s", issue.Text)
		}
		source, err := tree.ReadFile(issue.Pos.Filename)
		if err != nil {
			return nil, err
		}
		lines := strings.Split(string(source), "\n")
		from, to := issue.LineRange.From, issue.LineRange.To
		if from <= 0 || to < from || to > len(lines) {
			return nil, errors.New("invalid clone source range")
		}
		fragment := strings.Join(lines[from-1:to], "\n")
		// Token text preserves names and literals while ignoring comments and offsets.
		set := token.NewFileSet()
		var scan scanner.Scanner
		var scanErr error
		scan.Init(set.AddFile("fragment", -1, len(fragment)), []byte(fragment), func(pos token.Position, msg string) { scanErr = fmt.Errorf("%s: %s", pos, msg) }, 0)
		var canonical strings.Builder
		for {
			_, tok, text := scan.Scan()
			if tok == token.EOF {
				break
			}
			fmt.Fprintf(&canonical, "%d:%q;", tok, text)
		}
		if scanErr != nil {
			return nil, scanErr
		}
		key := fmt.Sprintf("%s|%s|%x", issue.Pos.Filename, peer[1], sha256.Sum256([]byte(canonical.String())))
		findings = append(findings, finding{Key: key, File: issue.Pos.Filename, Line: issue.Pos.Line, Detail: issue.Text})
	}
	return findings, nil
}
