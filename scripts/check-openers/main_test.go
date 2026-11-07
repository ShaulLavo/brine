package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func TestOpenerReferences(t *testing.T) {
	functions := map[string]*ast.FuncDecl{}
	file, err := parser.ParseFile(token.NewFileSet(), "store.go", `package store
 func ReadInventoryState() { OpenReadOnly() }
 func ReadGeneration() { ReadInventoryState() }
 func ReadUDPGrantHeld() { OpenSnapshotReadOnly() }
 func NewExportedBypass() { ReadGeneration() }
 func EmptyStateDirectory() { readMetadata() }
 `, 0)
	if err != nil {
		t.Fatal(err)
	}
	collectFunctions(file, functions)
	opening := openingFunctions(functions)
	for _, selector := range []string{"Open", "OpenContext", "OpenReadOnly", "OpenSnapshotReadOnly", "OpenPreviewReadOnly", "ReadInventoryState", "ReadGeneration", "ReadUDPGrantHeld", "NewExportedBypass"} {
		assertRejectedReference(t, selector, opening)
	}
	assertAllowedReference(t, opening)
}

func assertRejectedReference(t *testing.T, name string, opening map[string]bool) {
	t.Helper()
	for _, expression := range []string{"s." + name + "()", "_ = s." + name} {
		source := `package bypass; import s "` + storeImport + `"; func run() { ` + expression + ` }`
		file, err := parser.ParseFile(token.NewFileSet(), "bypass.go", source, 0)
		if err != nil {
			t.Fatal(err)
		}
		findings := fileFindings("internal/inventory/bypass.go", file, opening)
		if len(findings) != 1 || !strings.Contains(findings[0], name) {
			t.Fatal(expression, findings)
		}
	}
}

func assertAllowedReference(t *testing.T, opening map[string]bool) {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "reader.go", `package read; import "`+storeImport+`"; func run() { store.EmptyStateDirectory() }`, 0)
	if err != nil {
		t.Fatal(err)
	}
	if findings := fileFindings("internal/host/read.go", file, opening); len(findings) != 0 {
		t.Fatal(findings)
	}
}

func TestRealInventoryBypassFixture(t *testing.T) {
	findings, err := checkOpeners("testdata/inventory-bypass")
	if err != nil || len(findings) != 3 {
		t.Fatal(findings, err)
	}
	for _, name := range []string{"ReadInventoryState", "ReadGeneration", "ForwardedInventory"} {
		if !strings.Contains(strings.Join(findings, "\n"), name) {
			t.Fatal("missing exported indirect opener", name, findings)
		}
	}
}
