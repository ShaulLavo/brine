//go:build linux

package host

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestMetadataOnlyRestoreCallersUseDisposableLifetime(t *testing.T) {
	for _, name := range []string{"archive_linux.go", "archive_maintenance_linux.go", "archive_recovery_linux.go", "data_init_linux.go"} {
		assertMetadataRestoreLifetime(t, name)
	}
}

func assertMetadataRestoreLifetime(t *testing.T, name string) {
	t.Helper()
	tree, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	retained, disposable := 0, 0
	ast.Inspect(tree, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if ok && selector.Sel.Name == "VerifySQLite" {
			retained++
		}
		identifier, ok := call.Fun.(*ast.Ident)
		if ok && identifier.Name == "checkSQLiteRestore" {
			disposable++
		}
		return true
	})
	if retained != 0 || disposable != 1 {
		t.Errorf("%s: metadata-only caller retains verification output: retained=%d disposable=%d", name, retained, disposable)
	}
}
