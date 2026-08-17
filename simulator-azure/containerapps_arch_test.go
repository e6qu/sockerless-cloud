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
// this simulator starts to one rule: the platform it hands the container engine
// is read off the image's own manifest, never chosen by the simulator.
//
// This is a structural check rather than a runtime one because the failure it
// guards is a source-level habit, and because a runtime check would only ever
// cover the one workload family the test happened to start. It reads the
// package's syntax tree rather than its text: a substring search for one
// spelling of one hardcoded platform is satisfied by "linux/" + "arm64", by a
// host-derived value, and by any file the search did not name — and a
// host-derived platform is precisely the defect this found in the Azure
// Container Instances group launcher, in a file the old two-file text search
// never read.
func TestWorkloadContainersDeriveTheirPlatformFromTheImage(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse the simulator package: %v", err)
	}

	// The identifiers a platform may legitimately come from: the helper that
	// inspects the image manifest, and the local variable each caller assigns
	// its result to.
	const platformHelper = "localImagePlatform"
	derived := map[string]bool{}
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
					ident, ok := call.Fun.(*ast.Ident)
					if !ok || ident.Name != platformHelper {
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
					if ident, ok := v.Fun.(*ast.Ident); !ok || ident.Name != platformHelper {
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

	// The walk has to have found the workload launchers; a parse that matched
	// nothing would report every rule satisfied.
	if found < 5 {
		t.Fatalf("only %d ContainerConfig literals were found in the package — the syntax walk is not reaching the workload launchers", found)
	}
}
