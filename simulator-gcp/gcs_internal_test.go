package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"testing"
)

func TestGCSObjectStoreWritesUsePersistenceHelper(t *testing.T) {
	src, err := os.ReadFile("gcs.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "gcs.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Put" {
				return true
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok || (ident.Name != "objects" && ident.Name != "gcsObjects") {
				return true
			}
			// persistGCSObject is the payload write path (also writes the
			// host backing file). persistGCSObjectMetadata is the explicit
			// metadata-only path (store-only, no payload/generation change).
			if fn.Name.Name != "persistGCSObject" && fn.Name.Name != "persistGCSObjectMetadata" {
				t.Fatalf("%s calls %s.Put directly; use persistGCSObject (payload) or persistGCSObjectMetadata (metadata-only) for GCS object writes",
					fset.Position(call.Pos()), ident.Name)
			}
			return true
		})
	}
}
