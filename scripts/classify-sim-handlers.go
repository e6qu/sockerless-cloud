//go:build ignore

// classify-sim-handlers reports, for every route a simulator registers, whether
// the handler behind it does real work. Run from the repository root:
//
//	go run scripts/classify-sim-handlers.go <dir>...
//
// A surface table that records only "a route is mounted" cannot tell an
// implemented operation from one that answers and does nothing. Both look
// identical to a client that only checks the status code, and identical to the
// coverage probe — which is how five Cloud Storage object ACL methods counted
// as covered for as long as that gate existed. This reports the distinction the
// route alone cannot carry:
//
//	state  — the handler reaches simulator state: it reads or writes a store,
//	         directly or through a function in its own package. This is an
//	         operation that remembers what it did.
//	static — the handler answers without touching state. That is correct for a
//	         published catalog (machine types, region lists) or an echo the
//	         cloud itself computes without stored data, and it is the shape a
//	         stub also has — so the table shows it rather than hiding it inside
//	         a tick.
//	501    — the handler answers NotImplemented: a wire-visible gap.
//
// Output is one tab-separated `file:line<TAB>class` row per registration, which
// scripts/seed-surface-tables.sh joins onto the rows it extracts.
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

// storeOps are the store methods that reach stored state: the sim.Store
// interface plus Upsert, which only the concrete MemoryStore and SQLiteStore
// carry. Leaving Upsert out read EnableEbsEncryptionByDefault — which records
// the account's default through it — as touching nothing.
var storeOps = map[string]bool{
	"Get": true, "Put": true, "Update": true, "Delete": true, "Upsert": true,
	"List": true, "Filter": true, "Len": true, "Generation": true,
}

// registrars are the calls that mount a route.
var registrars = map[string]bool{
	"HandleFunc": true, "Register": true, "RegisterVersioned": true, "Handle": true,
}

type pkg struct {
	fset  *token.FileSet
	funcs map[string]*ast.FuncDecl
	files []*ast.File
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: classify-sim-handlers <dir>...")
		os.Exit(2)
	}
	var rows []string
	for _, root := range os.Args[1:] {
		p, err := load(root)
		if err != nil {
			fmt.Fprintf(os.Stderr, "classify-sim-handlers: %v\n", err)
			os.Exit(1)
		}
		rows = append(rows, p.classify()...)
	}
	sort.Strings(rows)
	for _, row := range rows {
		fmt.Println(row)
	}
}

func load(root string) (*pkg, error) {
	p := &pkg{fset: token.NewFileSet(), funcs: map[string]*ast.FuncDecl{}}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(p.fset, filepath.Join(root, name), nil, 0)
		if err != nil {
			return nil, err
		}
		p.files = append(p.files, file)
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Body != nil {
				p.funcs[fn.Name.Name] = fn
			}
		}
	}
	return p, nil
}

// classify walks every registration and reports the class of its handler.
func (p *pkg) classify() []string {
	var rows []string
	for _, file := range p.files {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !registrars[sel.Sel.Name] || len(call.Args) < 2 {
				return true
			}
			handler := call.Args[len(call.Args)-1]
			position := p.fset.Position(call.Pos())
			rows = append(rows, fmt.Sprintf("%s:%d\t%s",
				filepath.ToSlash(position.Filename), position.Line, p.classifyHandler(handler)))
			return true
		})
	}
	return rows
}

// classifyHandler resolves a handler expression to a body and reports its class.
func (p *pkg) classifyHandler(handler ast.Expr) string {
	var body *ast.BlockStmt
	switch h := handler.(type) {
	case *ast.FuncLit:
		body = h.Body
	case *ast.Ident:
		fn, ok := p.funcs[h.Name]
		if !ok {
			// A handler this package does not declare — a method value or an
			// import. Nothing to read, so nothing is claimed about it.
			return "unknown"
		}
		body = fn.Body
	case *ast.CallExpr:
		// A decorated handler: cloudTrailRecordedREST("CreateApp", …, handleX).
		// The wrapper and the handler it wraps both run, so the registration is
		// whatever the strongest of them is.
		touches, notImplemented, known := p.classifyWrapped(h)
		switch {
		case touches:
			return "state"
		case notImplemented:
			return "501"
		case known:
			return "static"
		default:
			return "unknown"
		}
	default:
		return "unknown"
	}
	touches, notImplemented := p.walk(body, map[string]bool{}, 0)
	switch {
	case touches:
		return "state"
	case notImplemented:
		return "501"
	default:
		return "static"
	}
}

// classifyWrapped reads a decorated registration: the wrapper's own body plus
// every argument that resolves to a handler this package declares. known is
// false when neither could be read, so the row stays honest about that.
func (p *pkg) classifyWrapped(call *ast.CallExpr) (touches, notImplemented, known bool) {
	consider := func(body *ast.BlockStmt) {
		if body == nil {
			return
		}
		known = true
		bodyTouches, bodyNotImplemented := p.walk(body, map[string]bool{}, 0)
		touches = touches || bodyTouches
		notImplemented = notImplemented || bodyNotImplemented
	}
	if ident, ok := call.Fun.(*ast.Ident); ok {
		if fn, declared := p.funcs[ident.Name]; declared {
			consider(fn.Body)
		}
	}
	for _, arg := range call.Args {
		switch a := arg.(type) {
		case *ast.Ident:
			if fn, declared := p.funcs[a.Name]; declared {
				consider(fn.Body)
			}
		case *ast.FuncLit:
			consider(a.Body)
		case *ast.CallExpr:
			innerTouches, innerNotImplemented, innerKnown := p.classifyWrapped(a)
			touches = touches || innerTouches
			notImplemented = notImplemented || innerNotImplemented
			known = known || innerKnown
		}
	}
	return touches, notImplemented, known
}

// walk reports whether a body reaches stored state, and whether it answers
// NotImplemented, following calls to functions the package declares. The depth
// bound keeps a cycle or a deep helper chain from running away; state reached
// deeper than that reads as static, which understates rather than overstates.
func (p *pkg) walk(body *ast.BlockStmt, seen map[string]bool, depth int) (touches, notImplemented bool) {
	if body == nil || depth > 6 {
		return false, false
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.SelectorExpr:
			if storeOps[node.Sel.Name] {
				// A store operation is a selector on a store value, so the
				// method name alone is the signal available without types.
				if _, isCall := node.X.(*ast.Ident); isCall {
					touches = true
				}
			}
		case *ast.Ident:
			if node.Name == "StatusNotImplemented" {
				notImplemented = true
			}
		case *ast.BasicLit:
			if node.Value == `"NotImplemented"` {
				notImplemented = true
			}
		case *ast.CallExpr:
			if ident, ok := node.Fun.(*ast.Ident); ok && !seen[ident.Name] {
				if fn, declared := p.funcs[ident.Name]; declared {
					seen[ident.Name] = true
					innerTouches, innerNotImplemented := p.walk(fn.Body, seen, depth+1)
					touches = touches || innerTouches
					notImplemented = notImplemented || innerNotImplemented
				}
			}
		}
		return true
	})
	return touches, notImplemented
}
