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
// It also resolves the route each registration mounts, including one composed
// from a version prefix a caller passes or from a constant, which a regular
// expression over the source cannot read. Output is one tab-separated
// `file:line<TAB>class<TAB>route<TAB>handler` row per mounted route, which
// scripts/seed-surface-tables.sh turns into the table rows.
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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
	// consts holds package-level string constants that routes are built from.
	consts map[string]string
	// callSites maps a function name to the argument lists it is called with,
	// so a route composed from a parameter resolves to what callers pass.
	callSites map[string][][]ast.Expr
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
	p := &pkg{
		fset:      token.NewFileSet(),
		funcs:     map[string]*ast.FuncDecl{},
		consts:    map[string]string{},
		callSites: map[string][][]ast.Expr{},
	}
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
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil && d.Body != nil {
					p.funcs[d.Name.Name] = d
				}
			case *ast.GenDecl:
				if d.Tok == token.CONST {
					collectStringConsts(d, func(name, value string) { p.consts[name] = value })
				}
			}
		}
	}
	// Index call sites once the package is parsed, so a registrar called from
	// another file resolves.
	for _, file := range p.files {
		ast.Inspect(file, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if ident, ok := call.Fun.(*ast.Ident); ok {
					p.callSites[ident.Name] = append(p.callSites[ident.Name], call.Args)
				}
			}
			return true
		})
	}
	return p, nil
}

// collectStringConsts reports each string constant a declaration binds.
func collectStringConsts(decl *ast.GenDecl, emit func(name, value string)) {
	for _, spec := range decl.Specs {
		value, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		for i, name := range value.Names {
			if i >= len(value.Values) {
				continue
			}
			lit, ok := value.Values[i].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			if unquoted, err := strconv.Unquote(lit.Value); err == nil {
				emit(name.Name, unquoted)
			}
		}
	}
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
			class := p.classifyHandler(handler)
			name := handlerName(handler)
			for _, route := range p.routes(call, file) {
				rows = append(rows, fmt.Sprintf("%s:%d\t%s\t%s\t%s",
					filepath.ToSlash(position.Filename), position.Line, class, route, name))
			}
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

// handlerName names the handler a registration mounts, for the table cell that
// points a reader at it. A decorated registration names the handler it wraps.
func handlerName(handler ast.Expr) string {
	switch h := handler.(type) {
	case *ast.Ident:
		return h.Name
	case *ast.CallExpr:
		for i := len(h.Args) - 1; i >= 0; i-- {
			if ident, ok := h.Args[i].(*ast.Ident); ok {
				return ident.Name
			}
		}
		if ident, ok := h.Fun.(*ast.Ident); ok {
			return ident.Name
		}
	}
	return "func"
}

// routes reports every route string a registration mounts.
//
// Most are one literal. A registrar serving a surface under several version
// prefixes takes the prefix as a parameter and concatenates it —
// `srv.HandleFunc("GET "+prefix+"/projects/{project}/instances", …)` — and its
// callers pass literals. Reading only the literal missed a quarter of the
// registered surface, so a table's silence about an op meant nothing.
func (p *pkg) routes(call *ast.CallExpr, file *ast.File) []string {
	// RegisterVersioned("2014-11-13", "Action", handler) names the action
	// second; every other registrar names the route first.
	at := 0
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "RegisterVersioned" {
		at = 1
	}
	if at >= len(call.Args) {
		return nil
	}
	enclosing := p.enclosingFunc(call, file)
	bindings := p.paramLiterals(enclosing)
	for name, value := range localConsts(enclosing) {
		if _, taken := bindings[name]; !taken {
			bindings[name] = []string{value}
		}
	}
	resolved := p.evalString(call.Args[at], bindings)
	sort.Strings(resolved)
	return resolved
}

// evalString evaluates a string expression to every value it can take. An
// operand it cannot read collapses the expression to nothing, so an
// unresolvable route is reported as absent rather than as a wrong string.
func (p *pkg) evalString(expr ast.Expr, bindings map[string][]string) []string {
	switch e := expr.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return nil
		}
		value, err := strconv.Unquote(e.Value)
		if err != nil {
			return nil
		}
		return []string{value}
	case *ast.Ident:
		if values, ok := bindings[e.Name]; ok {
			return values
		}
		if value, ok := p.consts[e.Name]; ok {
			return []string{value}
		}
		return nil
	case *ast.ParenExpr:
		return p.evalString(e.X, bindings)
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return nil
		}
		left, right := p.evalString(e.X, bindings), p.evalString(e.Y, bindings)
		if len(left) == 0 || len(right) == 0 {
			return nil
		}
		var out []string
		for _, l := range left {
			for _, r := range right {
				out = append(out, l+r)
			}
		}
		return out
	}
	return nil
}

// paramLiterals maps each string parameter of a function to the literal values
// its callers pass.
func (p *pkg) paramLiterals(fn *ast.FuncDecl) map[string][]string {
	bindings := map[string][]string{}
	if fn == nil || fn.Type.Params == nil {
		return bindings
	}
	index, position := map[string]int{}, 0
	for _, field := range fn.Type.Params.List {
		for _, name := range field.Names {
			if ident, ok := field.Type.(*ast.Ident); ok && ident.Name == "string" {
				index[name.Name] = position
			}
			position++
		}
	}
	for name, at := range index {
		seen := map[string]bool{}
		for _, args := range p.callSites[fn.Name.Name] {
			if at >= len(args) {
				continue
			}
			lit, ok := args[at].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil || seen[value] {
				continue
			}
			seen[value] = true
			bindings[name] = append(bindings[name], value)
		}
	}
	return bindings
}

// localConsts collects the string constants declared inside a function, which
// a registrar uses to name one long path it mounts under several methods.
func localConsts(fn *ast.FuncDecl) map[string]string {
	out := map[string]string{}
	if fn == nil || fn.Body == nil {
		return out
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if decl, ok := n.(*ast.GenDecl); ok && decl.Tok == token.CONST {
			collectStringConsts(decl, func(name, value string) { out[name] = value })
		}
		return true
	})
	return out
}

// enclosingFunc returns the function declaration a node sits inside.
func (p *pkg) enclosingFunc(target ast.Node, file *ast.File) *ast.FuncDecl {
	var found *ast.FuncDecl
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		if fn.Pos() <= target.Pos() && target.Pos() <= fn.End() {
			found = fn
		}
	}
	return found
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
