//go:build ignore

// check-store-scans reports a store read in full on a path that every request
// pays. Run from the repository root:
//
//	go run scripts/check-store-scans.go <dir>...
//
// A CPU profile of the deployed AWS simulator, taken while twelve concurrent
// requests were in flight against one load-balanced application, attributed
// 84.8% of all its CPU to a single function — and 99.7% of that to one
// `ecsTasks.List()`, JSON-decoding every stored Amazon ECS task, stopped ones
// included, once per proxied request, to find the one holding a target's ENI
// address. The guest has two vCPUs, so the whole data plane ran at an
// effective concurrency of two: a static health endpoint behind the load
// balancer answered in 1.3s where a directly-proxied one answered in 0.13s,
// and it grew linearly from there. It surfaced as thirty-second browser
// timeouts, because a page load fans out over dozens of subresources and every
// one paid the scan.
//
// What makes it a class rather than an incident is the second one. The load
// balancer's own host lookup had the identical shape on a hotter path still —
// a WrapHandler middleware, so every request into the simulator paid it before
// any handler ran, an Amazon DynamoDB call as much as a proxied page load —
// and it was invisible only because a deployment holds a handful of load
// balancers against a few hundred tasks. Nothing but a profile of a live
// deployment would have found either.
//
// So the scans this reports are the ones on that kind of path, not every scan
// in the tree. A control-plane Describe that reads a table once per API call
// costs one scan per call; a scan inside a handler wrapper costs one per
// request in the entire process, and a scan on the proxy path costs one per
// proxied byte-stream. Those are the two seeds below, followed transitively
// through the package's own calls.
//
// The fix shape is an index rebuilt only when the store's Generation() moves —
// see ecsRunningTaskENIs and elbv2LoadBalancerFromDataPlaneHost, and the tests
// beside them, which count reads of the store rather than timing anything.
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type finding struct {
	pos   string
	fn    string
	store string
	why   string
}

func main() {
	roots := os.Args[1:]
	if len(roots) == 0 {
		roots = []string{"."}
	}
	fset := token.NewFileSet()
	byDir := map[string][]string{}
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				switch filepath.Base(path) {
				case "node_modules", ".git", "dist", ".build", "testdata":
					return filepath.SkipDir
				}
				return nil
			}
			// A test that reads every row to assert about it is not a request
			// path.
			if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
				byDir[filepath.Dir(path)] = append(byDir[filepath.Dir(path)], path)
			}
			return nil
		})
	}
	dirs := make([]string, 0, len(byDir))
	for dir := range byDir {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)

	var findings []finding
	for _, dir := range dirs {
		files := map[string]*ast.File{}
		for _, path := range byDir[dir] {
			if file, err := parser.ParseFile(fset, path, nil, 0); err == nil {
				files[path] = file
			}
		}
		findings = append(findings, scanPackage(fset, files)...)
	}
	sort.Slice(findings, func(i, j int) bool { return findings[i].pos < findings[j].pos })
	for _, f := range findings {
		fmt.Printf("store-scan %s\t%s\t%s\t%s\n", f.pos, f.fn, f.store, f.why)
	}
	fmt.Fprintf(os.Stderr, "store-scan findings: %d\n", len(findings))
}

func scanPackage(fset *token.FileSet, files map[string]*ast.File) []finding {
	bodies := map[string]*ast.FuncDecl{}
	pathOf := map[string]string{}
	for path, file := range files {
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
				bodies[fn.Name.Name] = fn
				pathOf[fn.Name.Name] = path
			}
		}
	}

	// Seeds: the functions a request reaches before, or instead of, its own
	// handler. A WrapHandler middleware runs for every request in the process;
	// a data-plane handler runs for every request the simulator proxies.
	seeds := map[string]string{}
	for path, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "WrapHandler" {
				return true
			}
			for _, name := range calledNames(call.Args[0]) {
				seeds[name] = "reached from a WrapHandler middleware, so every request pays it"
			}
			_ = path
			return true
		})
	}
	for name := range bodies {
		if strings.Contains(name, "DataPlane") && strings.HasPrefix(name, "handle") {
			seeds[name] = "reached from a data-plane handler, so every proxied request pays it"
		}
	}

	// Close the seed set over the package's own calls.
	reachable := map[string]string{}
	for name, why := range seeds {
		reachable[name] = why
	}
	for changed := true; changed; {
		changed = false
		for name, why := range map[string]string(reachable) {
			fn, ok := bodies[name]
			if !ok {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				id, ok := call.Fun.(*ast.Ident)
				if !ok {
					return true
				}
				if _, known := bodies[id.Name]; !known {
					return true
				}
				if _, already := reachable[id.Name]; already {
					return true
				}
				reachable[id.Name] = why
				changed = true
				return true
			})
		}
	}

	names := make([]string, 0, len(reachable))
	for name := range reachable {
		names = append(names, name)
	}
	sort.Strings(names)

	var out []finding
	for _, name := range names {
		fn, ok := bodies[name]
		if !ok {
			continue
		}
		amortized := generationGatedStores(fn)
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			store, receiver, ok := fullStoreRead(call)
			if !ok {
				return true
			}
			// A scan in a function that also reads the same store's
			// Generation() is the amortized rebuild of a generation-keyed
			// index — the fix this gate exists to demand, not the defect. It
			// runs once per store change, not once per request, and counting
			// it means the floor can never reach zero however completely the
			// class is converted.
			if amortized[receiver] {
				return true
			}
			pos := fset.Position(call.Pos())
			out = append(out, finding{
				pos:   fmt.Sprintf("%s:%d", pathOf[name], pos.Line),
				fn:    name,
				store: store,
				why:   reachable[name],
			})
			return true
		})
	}
	return out
}

// generationGatedStores reports the stores whose Generation() the function
// reads — the marker of a generation-keyed index rebuild.
func generationGatedStores(fn *ast.FuncDecl) map[string]bool {
	gated := map[string]bool{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Generation" {
			return true
		}
		if receiver, ok := sel.X.(*ast.Ident); ok {
			gated[receiver.Name] = true
		}
		return true
	})
	return gated
}

// calledNames returns the function names an expression passed to WrapHandler
// eventually calls: the middleware is a closure, and what matters is what it
// reaches.
func calledNames(expr ast.Expr) []string {
	var names []string
	ast.Inspect(expr, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok {
			names = append(names, id.Name)
		}
		return true
	})
	return names
}

// fullStoreRead reports whether a call reads a whole store — List or Filter on
// a package-level collection, each of which decodes every row it holds.
func fullStoreRead(call *ast.CallExpr) (string, string, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", "", false
	}
	if sel.Sel.Name != "List" && sel.Sel.Name != "Filter" {
		return "", "", false
	}
	receiver, ok := sel.X.(*ast.Ident)
	if !ok {
		return "", "", false
	}
	// Package-level stores are plural nouns. A local slice or a method on a
	// receiver field is a collection somebody already has in hand.
	if !strings.HasSuffix(receiver.Name, "s") {
		return "", "", false
	}
	return receiver.Name + "." + sel.Sel.Name + "()", receiver.Name, true
}
