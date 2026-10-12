// migrate-archive-unions rewrites concrete SQLite archive construction to the
// explicit arm. It is safe to rerun after those constructors have been migrated.
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type edit struct {
	start int
	end   int
	value string
}

func main() {
	root := flag.String("root", "internal", "source directory")
	write := flag.Bool("write", false, "apply edits")
	flag.Parse()
	err := filepath.WalkDir(*root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || filepath.ToSlash(path) == "internal/archive/engine.go" {
			return nil
		}
		return migrate(path, *write)
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func migrate(path string, write bool) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, raw, parser.SkipObjectResolution)
	if err != nil {
		return err
	}
	parents := parentNodes(file)
	var edits []edit
	ast.Inspect(file, func(node ast.Node) bool {
		literal, ok := node.(*ast.CompositeLit)
		if !ok || len(literal.Elts) == 0 {
			return true
		}
		kind, qualifier, position := archiveLiteral(file, literal, parents[literal])
		if kind == "" {
			return true
		}
		begin, end := fset.Position(literal.Pos()).Offset, fset.Position(literal.End()).Offset
		if literal.Type == nil {
			edits = append(edits, edit{begin, begin, qualifier + "SQLite" + kind + "Variant(" + qualifier + "SQLite" + kind}, edit{end, end, ")"})
			return true
		}
		typePos := fset.Position(position).Offset
		edits = append(edits, edit{typePos, typePos + len(kind), "SQLite" + kind})
		if strings.HasSuffix(path, "_test.go") {
			edits = append(edits, edit{begin, begin, qualifier + "SQLite" + kind + "Variant("}, edit{end, end, ")"})
		}
		return true
	})
	if len(edits) == 0 {
		return nil
	}
	slices.SortFunc(edits, func(a, b edit) int {
		if a.start != b.start {
			return b.start - a.start
		}
		return b.end - a.end
	})
	for _, change := range edits {
		raw = append(append(append([]byte{}, raw[:change.start]...), []byte(change.value)...), raw[change.end:]...)
	}
	fmt.Printf("%s\t%d edits\n", path, len(edits))
	if !write {
		return nil
	}
	return os.WriteFile(path, raw, 0600)
}

func parentNodes(file *ast.File) map[ast.Node]ast.Node {
	parents := map[ast.Node]ast.Node{}
	var stack []ast.Node
	ast.Inspect(file, func(node ast.Node) bool {
		if node == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		if len(stack) > 0 {
			parents[node] = stack[len(stack)-1]
		}
		stack = append(stack, node)
		return true
	})
	return parents
}

func archiveLiteral(file *ast.File, literal *ast.CompositeLit, parent ast.Node) (string, string, token.Pos) {
	typeExpr := literal.Type
	if typeExpr == nil {
		container, ok := parent.(*ast.CompositeLit)
		if !ok {
			return "", "", 0
		}
		array, ok := container.Type.(*ast.ArrayType)
		if !ok {
			return "", "", 0
		}
		typeExpr = array.Elt
	}
	if name, ok := typeExpr.(*ast.Ident); ok && file.Name.Name == "archive" && (name.Name == "Database" || name.Name == "Coverage") {
		return name.Name, "", name.Pos()
	}
	selector, ok := typeExpr.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Database" && selector.Sel.Name != "Coverage" {
		return "", "", 0
	}
	name, ok := selector.X.(*ast.Ident)
	if !ok || name.Name != "archive" {
		return "", "", 0
	}
	return selector.Sel.Name, "archive.", selector.Sel.Pos()
}
