// Command check-openers rejects control-store opening outside the admission gate.
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

const storeImport = "github.com/ShaulLavo/brine/internal/store"

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	findings, err := checkOpeners(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if len(findings) == 0 {
		return
	}
	fmt.Fprintln(os.Stderr, strings.Join(findings, "\n"))
	os.Exit(1)
}

func checkOpeners(root string) ([]string, error) {
	functions, err := storeFunctions(filepath.Join(root, "internal/store"))
	if err != nil {
		return nil, err
	}
	opening := openingFunctions(functions)
	var findings []string
	// #nosec G703 -- Explicit source checkout selected by the local check command.
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		return checkEntry(root, path, entry, err, opening, &findings)
	})
	slices.Sort(findings)
	return findings, err
}

func checkEntry(root, path string, entry fs.DirEntry, err error, opening map[string]bool, findings *[]string) error {
	if err != nil {
		return err
	}
	if entry.IsDir() {
		if entry.Name() == ".git" || entry.Name() == "testdata" || strings.HasPrefix(entry.Name(), ".tmp") {
			return filepath.SkipDir
		}
		return nil
	}
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return err
	}
	relative = filepath.ToSlash(relative)
	if !sourceFile(relative) || strings.HasPrefix(relative, "internal/store/") || relative == "internal/host/admission_gate_linux.go" {
		return nil
	}
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		return err
	}
	*findings = append(*findings, fileFindings(relative, file, opening)...)
	return nil
}

func sourceFile(name string) bool {
	return strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go")
}

func storeFunctions(root string) (map[string]*ast.FuncDecl, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	functions := map[string]*ast.FuncDecl{}
	for _, entry := range entries {
		if !sourceFile(entry.Name()) {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, entry.Name()), nil, 0)
		if err != nil {
			return nil, err
		}
		collectFunctions(file, functions)
	}
	return functions, nil
}

func collectFunctions(file *ast.File, functions map[string]*ast.FuncDecl) {
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if ok && function.Recv == nil {
			functions[function.Name.Name] = function
		}
	}
}

func openingFunctions(functions map[string]*ast.FuncDecl) map[string]bool {
	opening := map[string]bool{"Open": true, "OpenContext": true, "OpenReadOnly": true, "OpenSnapshotReadOnly": true, "OpenPreviewReadOnly": true}
	for markOpening(functions, opening) {
	}

	return opening
}

func referencesOpening(node ast.Node, opening map[string]bool) bool {
	found := false
	ast.Inspect(node, func(node ast.Node) bool {
		identifier, ok := node.(*ast.Ident)
		if ok && opening[identifier.Name] {
			found = true
		}
		return !found
	})
	return found
}

func fileFindings(path string, file *ast.File, opening map[string]bool) []string {
	aliases := map[string]bool{}
	findings := []string{}
	for _, spec := range file.Imports {
		name, err := strconv.Unquote(spec.Path.Value)
		if err != nil || name != storeImport {
			continue
		}
		alias := "store"
		if spec.Name != nil {
			alias = spec.Name.Name
		}
		aliases[alias] = true
	}
	if aliases["."] {
		return []string{path + ": dot import of control store bypasses opener boundary"}
	}
	ast.Inspect(file, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if !ok || !opening[selector.Sel.Name] {
			return true
		}
		identifier, ok := selector.X.(*ast.Ident)
		if ok && aliases[identifier.Name] {
			findings = append(findings, fmt.Sprintf("%s: ungated store opener %s", path, selector.Sel.Name))
		}
		return true
	})
	return findings
}

func markOpening(functions map[string]*ast.FuncDecl, opening map[string]bool) bool {
	changed := false
	for name, function := range functions {
		if opening[name] || !referencesOpening(function.Body, opening) {
			continue
		}
		opening[name], changed = true, true
	}
	return changed
}
