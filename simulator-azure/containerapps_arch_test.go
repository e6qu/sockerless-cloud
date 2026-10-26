package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strings"
	"testing"
)

// TestWorkloadContainersDeriveTheirPlatformFromTheImage holds every workload
// launcher to one rule: the platform handed to the engine comes from the
// image's manifest, never from the simulator. It walks the package's syntax
// tree, because a text search misses "linux/" + "arm64", host-derived values
// and files it does not name.
func TestWorkloadContainersDeriveTheirPlatformFromTheImage(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse the simulator package: %v", err)
	}

	// A workload.Container names no platform: workload.StartGroup reads it off the
	// image with the same helper.
	const platformHelper = "workload.LocalImagePlatform"
	isPlatformHelper := func(fun ast.Expr) bool {
		sel, ok := fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "LocalImagePlatform" {
			return false
		}
		pkg, ok := sel.X.(*ast.Ident)
		return ok && pkg.Name == "workload"
	}
	derived := map[string]bool{}
	groupMembers := map[*ast.CompositeLit]bool{}
	found := 0

	for _, pkg := range pkgs {
		for path, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				assign, ok := n.(*ast.AssignStmt)
				if !ok {
					return true
				}
				for _, rhs := range assign.Rhs {
					call, ok := rhs.(*ast.CallExpr)
					if !ok {
						continue
					}
					if !isPlatformHelper(call.Fun) {
						continue
					}
					for _, lhs := range assign.Lhs {
						if name, ok := lhs.(*ast.Ident); ok {
							derived[name.Name] = true
						}
					}
				}
				return true
			})

			ast.Inspect(file, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok {
					return true
				}
				if sel, ok := lit.Type.(*ast.SelectorExpr); !ok || sel.Sel.Name != "Container" {
					return true
				}
				for _, elt := range lit.Elts {
					if kv, ok := elt.(*ast.KeyValueExpr); ok {
						if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "Config" {
							if cfg, ok := kv.Value.(*ast.CompositeLit); ok {
								groupMembers[cfg] = true
							}
						}
					}
				}
				return true
			})

			ast.Inspect(file, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok {
					return true
				}
				sel, ok := lit.Type.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "ContainerConfig" {
					return true
				}
				found++
				pos := fset.Position(lit.Pos())
				var value ast.Expr
				for _, elt := range lit.Elts {
					kv, ok := elt.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "Architecture" {
						value = kv.Value
					}
				}
				if groupMembers[lit] {
					if value != nil {
						t.Errorf("%s:%d: a workload.Container names its Architecture; workload.StartGroup reads it off the image",
							path, pos.Line)
					}
					return true
				}
				if value == nil {
					t.Errorf("%s:%d: a ContainerConfig starts a workload without naming its Architecture, so the engine picks one; read it off the image with %s",
						path, pos.Line, platformHelper)
					return true
				}
				switch v := value.(type) {
				case *ast.Ident:
					if !derived[v.Name] {
						t.Errorf("%s:%d: Architecture comes from %q, which is not assigned from %s — the platform must be the image's own, not the simulator's choice",
							path, pos.Line, v.Name, platformHelper)
					}
				case *ast.CallExpr:
					if !isPlatformHelper(v.Fun) {
						t.Errorf("%s:%d: Architecture is computed by a call other than %s", path, pos.Line, platformHelper)
					}
				default:
					t.Errorf("%s:%d: Architecture is a literal or expression the simulator chose (%T); it must be read off the image manifest with %s",
						path, pos.Line, value, platformHelper)
				}
				return true
			})
		}
	}

	// A parse that matched nothing would report every rule satisfied.
	if found < 5 {
		t.Fatalf("only %d ContainerConfig literals were found in the package — the syntax walk is not reaching the workload launchers", found)
	}
}
