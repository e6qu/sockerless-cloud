//go:build ignore

// check-fake-tests reports tests that cannot fail, or that can pass without
// exercising what they name. Run from the repository root:
//
//	go run scripts/check-fake-tests.go <dir>...
//
// Every detector decides from the syntax tree rather than from a grep, because
// the sweep that motivated this found that grep-based rules miss the instance
// that matters: the offending expression is in the third file, or is not a
// literal. Each detector was also calibrated against this repository before
// being trusted — the first run of `empty-table` reported twenty-four
// collections as permanently empty, every one of them a map filled by index
// assignment the detector could not see. A detector that reports findings
// nobody can act on is itself a fake test, so those were fixed before any
// finding here was acted on.
//
// Classes at zero are held at zero; the classes with a standing population
// carry a floor that may only fall. See scripts/check-fake-tests.sh.
//
// Detectors:
//
//	no-assertion       a Test function whose body makes no assertion of any kind
//	empty-table        a table-driven loop whose table is an empty literal
//	self-compare       an assert/require comparing an expression with itself
//	trivial-eventually a wait whose condition cannot be false
//	empty-subtest      t.Run with an empty body
//	fatal-in-goroutine t.Fatal* off the test goroutine, which does not fail the test
//	sleep-then-assert  a bare sleep standing in for synchronisation
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
	kind string
	pos  string
	name string
	note string
}

func main() {
	roots := os.Args[1:]
	if len(roots) == 0 {
		roots = []string{"."}
	}
	var findings []finding
	fset := token.NewFileSet()
	// Group by directory: a test's assertion often lives in a helper defined in
	// a sibling file of the same package, and a detector that cannot see it
	// reports every such test as asserting nothing.
	byDir := map[string][]string{}
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				base := filepath.Base(path)
				if base == "node_modules" || base == ".git" || base == "dist" || base == ".build" {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(path, ".go") {
				dir := filepath.Dir(path)
				byDir[dir] = append(byDir[dir], path)
			}
			return nil
		})
	}
	dirs := make([]string, 0, len(byDir))
	for dir := range byDir {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	for _, dir := range dirs {
		files := map[string]*ast.File{}
		for _, path := range byDir[dir] {
			file, perr := parser.ParseFile(fset, path, nil, parser.ParseComments)
			if perr != nil {
				continue
			}
			files[path] = file
		}
		asserting := assertingFuncs(files)
		identifying := identifyingFuncs(files)
		paths := make([]string, 0, len(files))
		for path := range files {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		for _, path := range paths {
			if !strings.HasSuffix(path, "_test.go") {
				continue
			}
			findings = append(findings, scan(fset, files[path], path, asserting, identifying)...)
		}
	}
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].kind != findings[j].kind {
			return findings[i].kind < findings[j].kind
		}
		return findings[i].pos < findings[j].pos
	})
	counts := map[string]int{}
	for _, f := range findings {
		counts[f.kind]++
		fmt.Printf("%-18s %s\t%s\t%s\n", f.kind, f.pos, f.name, f.note)
	}
	fmt.Fprintln(os.Stderr, "--- totals ---")
	kinds := make([]string, 0, len(counts))
	for k := range counts {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	for _, k := range kinds {
		fmt.Fprintf(os.Stderr, "%-18s %d\n", k, counts[k])
	}
}

func scan(fset *token.FileSet, file *ast.File, path string, asserting, identifying map[string]bool) []finding {
	var out []finding
	at := func(p token.Pos) string {
		pos := fset.Position(p)
		return fmt.Sprintf("%s:%d", path, pos.Line)
	}

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		isTest := strings.HasPrefix(fn.Name.Name, "Test") && fn.Recv == nil
		if isTest && !hasAssertion(fn.Body, asserting) {
			out = append(out, finding{"no-assertion", at(fn.Pos()), fn.Name.Name,
				"the function body makes no assertion"})
		}

		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.RangeStmt:
				if lit, ok := node.X.(*ast.CompositeLit); ok && len(lit.Elts) == 0 {
					out = append(out, finding{"empty-table", at(node.Pos()), fn.Name.Name,
						"the loop ranges over an empty literal, so the body never runs"})
				}
				if ident, ok := node.X.(*ast.Ident); ok {
					if lit := emptyLiteralFor(fn.Body, ident.Name); lit {
						out = append(out, finding{"empty-table", at(node.Pos()), fn.Name.Name,
							fmt.Sprintf("the loop ranges over %s, assigned an empty literal", ident.Name)})
					}
				}
			case *ast.CallExpr:
				sel, ok := node.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, _ := sel.X.(*ast.Ident)
				if pkg == nil {
					return true
				}
				switch {
				case (pkg.Name == "assert" || pkg.Name == "require") &&
					(sel.Sel.Name == "Equal" || sel.Sel.Name == "NotEqual" ||
						sel.Sel.Name == "EqualValues" || sel.Sel.Name == "Same"):
					if len(node.Args) >= 3 {
						a, b := render(fset, node.Args[1]), render(fset, node.Args[2])
						if a == b {
							out = append(out, finding{"self-compare", at(node.Pos()), fn.Name.Name,
								"both sides are " + a})
						}
					}
				case (pkg.Name == "assert" || pkg.Name == "require") &&
					(sel.Sel.Name == "Eventually" || sel.Sel.Name == "EventuallyWithT" ||
						sel.Sel.Name == "Never"):
					if len(node.Args) >= 2 {
						if lit, ok := node.Args[1].(*ast.FuncLit); ok && trivialCondition(lit) {
							out = append(out, finding{"trivial-eventually", at(node.Pos()), fn.Name.Name,
								"the wait's condition cannot be false"})
						}
					}
				case pkg.Name == "t" && sel.Sel.Name == "Run":
					if len(node.Args) >= 2 {
						if lit, ok := node.Args[1].(*ast.FuncLit); ok && len(lit.Body.List) == 0 {
							out = append(out, finding{"empty-subtest", at(node.Pos()), fn.Name.Name,
								"the subtest body is empty"})
						}
					}
				}
			case *ast.GoStmt:
				if lit, ok := node.Call.Fun.(*ast.FuncLit); ok {
					ast.Inspect(lit.Body, func(inner ast.Node) bool {
						call, ok := inner.(*ast.CallExpr)
						if !ok {
							return true
						}
						sel, ok := call.Fun.(*ast.SelectorExpr)
						if !ok {
							return true
						}
						recv, _ := sel.X.(*ast.Ident)
						if recv == nil || recv.Name != "t" {
							return true
						}
						if strings.HasPrefix(sel.Sel.Name, "Fatal") || strings.HasPrefix(sel.Sel.Name, "Skip") {
							out = append(out, finding{"fatal-in-goroutine", at(call.Pos()), fn.Name.Name,
								"t." + sel.Sel.Name + " off the test goroutine does not fail the test"})
						}
						return true
					})
				}
			}
			return true
		})

		out = append(out, sleepThenAssert(fset, fn, path)...)
		out = append(out, statusOnly(fset, fn, path)...)
		out = append(out, anyError(fset, fn, path, identifying)...)
	}
	return out
}

// emptyLiteralFor reports whether name is assigned an empty composite literal
// in this body and never appended to.
func emptyLiteralFor(body *ast.BlockStmt, name string) bool {
	assignedEmpty := false
	grown := false
	ast.Inspect(body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.AssignStmt:
			for i, lhs := range node.Lhs {
				// A map or slice filled by index — seen[x] = true — grows
				// without ever being reassigned. Reading only Ident targets
				// reported every such collection as permanently empty, which
				// was every hit this detector produced on its first run.
				if index, ok := lhs.(*ast.IndexExpr); ok {
					if id, ok := index.X.(*ast.Ident); ok && id.Name == name {
						grown = true
					}
					continue
				}
				ident, ok := lhs.(*ast.Ident)
				if !ok || ident.Name != name || i >= len(node.Rhs) {
					continue
				}
				if lit, ok := node.Rhs[i].(*ast.CompositeLit); ok && len(lit.Elts) == 0 {
					assignedEmpty = true
				}
			}
		case *ast.IncDecStmt:
			// seen[id]++ grows a counter map without any assignment at all.
			if index, ok := node.X.(*ast.IndexExpr); ok {
				if id, ok := index.X.(*ast.Ident); ok && id.Name == name {
					grown = true
				}
			}
		case *ast.CallExpr:
			// append(name, ...) anywhere — including inside a closure, or
			// assigned to a different variable — is growth.
			if id, ok := node.Fun.(*ast.Ident); ok && id.Name == "append" && len(node.Args) > 0 {
				if arg, ok := node.Args[0].(*ast.Ident); ok && arg.Name == name {
					grown = true
				}
			}
			// A collection handed to a function may be filled through the
			// reference it passes.
			for _, arg := range node.Args {
				if id, ok := arg.(*ast.Ident); ok && id.Name == name {
					grown = true
				}
			}
		case *ast.UnaryExpr:
			if node.Op == token.AND {
				if id, ok := node.X.(*ast.Ident); ok && id.Name == name {
					grown = true
				}
			}
		}
		return true
	})
	return assignedEmpty && !grown
}

// trivialCondition reports whether a wait closure's every return is a constant
// true, or a comparison that holds for any value (len(x) >= 0).
func trivialCondition(lit *ast.FuncLit) bool {
	returns := 0
	trivial := 0
	ast.Inspect(lit.Body, func(n ast.Node) bool {
		ret, ok := n.(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 1 {
			return true
		}
		returns++
		switch v := ret.Results[0].(type) {
		case *ast.Ident:
			if v.Name == "true" {
				trivial++
			}
		case *ast.BinaryExpr:
			if v.Op == token.GEQ {
				if basic, ok := v.Y.(*ast.BasicLit); ok && basic.Value == "0" {
					trivial++
				}
			}
		}
		return true
	})
	return returns > 0 && returns == trivial
}

// sleepThenAssert reports a bare sleep that is the only thing standing between
// an action and the assertion that reads its result.
func sleepThenAssert(fset *token.FileSet, fn *ast.FuncDecl, path string) []finding {
	var out []finding
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		block, ok := n.(*ast.BlockStmt)
		if !ok {
			return true
		}
		for i, stmt := range block.List {
			expr, ok := stmt.(*ast.ExprStmt)
			if !ok {
				continue
			}
			call, ok := expr.X.(*ast.CallExpr)
			if !ok {
				continue
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				continue
			}
			pkg, _ := sel.X.(*ast.Ident)
			if pkg == nil || pkg.Name != "time" || sel.Sel.Name != "Sleep" {
				continue
			}
			if i+1 >= len(block.List) {
				continue
			}
			if containsAssertion(block.List[i+1]) {
				pos := fset.Position(call.Pos())
				out = append(out, finding{"sleep-then-assert",
					fmt.Sprintf("%s:%d", path, pos.Line), fn.Name.Name,
					"a sleep is the only thing between the action and the assertion"})
			}
		}
		return true
	})
	return out
}

func containsAssertion(n ast.Node) bool {
	found := false
	ast.Inspect(n, func(inner ast.Node) bool {
		call, ok := inner.(*ast.CallExpr)
		if !ok {
			return true
		}
		if isAssertionCall(call) {
			found = true
		}
		return true
	})
	return found
}

func hasAssertion(body *ast.BlockStmt, asserting map[string]bool) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if isAssertionCall(call) || callsAsserting(call, asserting) {
			found = true
		}
		return true
	})
	return found
}

// callsAsserting reports whether the call reaches a helper in the same package
// that asserts, directly or through helpers of its own.
func callsAsserting(call *ast.CallExpr, asserting map[string]bool) bool {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return asserting[fun.Name]
	case *ast.SelectorExpr:
		return asserting[fun.Sel.Name]
	}
	return false
}

// assertingFuncs returns the names of every function in the package that
// asserts, closed transitively over calls between them: a test whose only
// assertion is three helpers deep still asserts.
func assertingFuncs(files map[string]*ast.File) map[string]bool {
	bodies := map[string]*ast.FuncDecl{}
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && fn.Body != nil {
				bodies[fn.Name.Name] = fn
			}
		}
	}
	asserting := map[string]bool{}
	for name, fn := range bodies {
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok && isAssertionCall(call) {
				asserting[name] = true
			}
			return true
		})
	}
	for changed := true; changed; {
		changed = false
		for name, fn := range bodies {
			if asserting[name] {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if callsAsserting(call, asserting) {
					asserting[name] = true
					changed = true
				}
				return true
			})
		}
	}
	return asserting
}

func isAssertionCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		// A bare helper call (checkFoo(t, ...)) counts: the assertion is one
		// level down, and treating it as none would drown the report.
		return false
	}
	recv, _ := sel.X.(*ast.Ident)
	if recv != nil {
		switch recv.Name {
		case "assert", "require":
			return true
		case "t", "b", "tb":
			switch {
			case strings.HasPrefix(sel.Sel.Name, "Error"),
				strings.HasPrefix(sel.Sel.Name, "Fatal"),
				strings.HasPrefix(sel.Sel.Name, "Skip"),
				sel.Sel.Name == "Fail", sel.Sel.Name == "FailNow":
				return true
			}
		}
	}
	return false
}

func render(fset *token.FileSet, e ast.Expr) string {
	var b strings.Builder
	_ = printExpr(&b, fset, e)
	return b.String()
}

func printExpr(b *strings.Builder, fset *token.FileSet, e ast.Expr) error {
	start := fset.Position(e.Pos())
	end := fset.Position(e.End())
	data, err := os.ReadFile(start.Filename)
	if err != nil {
		return err
	}
	if start.Offset < 0 || end.Offset > len(data) || start.Offset >= end.Offset {
		return nil
	}
	b.Write(data[start.Offset:end.Offset])
	return nil
}

// statusOnly reports a response whose status code is asserted and whose body is
// never read. A handler returning a constant, an empty list, or the request
// echoed back satisfies a status assertion, which is how two Azure subnet
// child collections stayed constants through a passing suite.
func statusOnly(fset *token.FileSet, fn *ast.FuncDecl, path string) []finding {
	var out []finding
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		block, ok := n.(*ast.BlockStmt)
		if !ok {
			return true
		}
		// Recorders whose Code is asserted somewhere in this block. Only a
		// success status counts: a test asserting 404 or 403 is asserting that
		// nothing was served, and has no body to read. A 2xx with an unread
		// body is the dangerous form — a handler returning a constant, an
		// empty list, or the request echoed back satisfies it.
		statusChecked := map[string]token.Pos{}
		bodyRead := map[string]bool{}
		ast.Inspect(block, func(inner ast.Node) bool {
			if bin, ok := inner.(*ast.BinaryExpr); ok {
				if sel, ok := bin.X.(*ast.SelectorExpr); ok && sel.Sel.Name == "Code" {
					if recv, ok := sel.X.(*ast.Ident); ok && successStatus(bin.Y) {
						if _, seen := statusChecked[recv.Name]; !seen {
							statusChecked[recv.Name] = sel.Pos()
						}
					}
				}
				if sel, ok := bin.Y.(*ast.SelectorExpr); ok && sel.Sel.Name == "Code" {
					if recv, ok := sel.X.(*ast.Ident); ok && successStatus(bin.X) {
						if _, seen := statusChecked[recv.Name]; !seen {
							statusChecked[recv.Name] = sel.Pos()
						}
					}
				}
			}
			if call, ok := inner.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					if pkg, ok := sel.X.(*ast.Ident); ok &&
						(pkg.Name == "assert" || pkg.Name == "require") &&
						(sel.Sel.Name == "Equal" || sel.Sel.Name == "EqualValues") &&
						len(call.Args) >= 3 {
						if code, ok := call.Args[2].(*ast.SelectorExpr); ok && code.Sel.Name == "Code" {
							if recv, ok := code.X.(*ast.Ident); ok && successStatus(call.Args[1]) {
								if _, seen := statusChecked[recv.Name]; !seen {
									statusChecked[recv.Name] = code.Pos()
								}
							}
						}
					}
				}
			}
			sel, ok := inner.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			recv, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			switch sel.Sel.Name {
			case "Body", "Result":
				bodyRead[recv.Name] = true
			}
			return true
		})
		for name, pos := range statusChecked {
			if bodyRead[name] {
				continue
			}
			p := fset.Position(pos)
			out = append(out, finding{"status-only",
				fmt.Sprintf("%s:%d", path, p.Line), fn.Name.Name,
				"the status of " + name + " is asserted and its body never read"})
		}
		return false // one report per outermost block is enough
	})
	return out
}

// anyError reports an error-path assertion that accepts any error at all. A
// transport failure, a 500, and a deserialisation error all satisfy a bare
// Error assertion, and none of them prove the service refused anything.
func anyError(fset *token.FileSet, fn *ast.FuncDecl, path string, identifying map[string]bool) []finding {
	var out []finding
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		block, ok := n.(*ast.BlockStmt)
		if !ok {
			return true
		}
		// Does this block identify an error anywhere — by code, message, or
		// errors.As/Is?
		identified := false
		var bare []token.Pos
		ast.Inspect(block, func(inner ast.Node) bool {
			call, ok := inner.(*ast.CallExpr)
			if !ok {
				return true
			}
			// A helper that names the error counts, however deep it sits:
			// requireSGErrorCode(t, err, "InvalidGroup.NotFound") identifies
			// the refusal exactly as an inline comparison would, and a
			// detector blind to it reported six such tests as accepting any
			// error.
			if callsAsserting(call, identifying) {
				identified = true
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, _ := sel.X.(*ast.Ident)
			if pkg == nil {
				return true
			}
			switch {
			case pkg.Name == "errors" && (sel.Sel.Name == "As" || sel.Sel.Name == "Is"):
				identified = true
			case (pkg.Name == "assert" || pkg.Name == "require") &&
				(sel.Sel.Name == "ErrorContains" || sel.Sel.Name == "ErrorAs" ||
					sel.Sel.Name == "ErrorIs" || sel.Sel.Name == "EqualError"):
				identified = true
			case (pkg.Name == "assert" || pkg.Name == "require") &&
				(sel.Sel.Name == "Contains" || sel.Sel.Name == "Equal"):
				// A message or code compared after the fact identifies it too.
				identified = true
			case (pkg.Name == "assert" || pkg.Name == "require") && sel.Sel.Name == "Error":
				bare = append(bare, call.Pos())
			}
			return true
		})
		if !identified {
			for _, pos := range bare {
				p := fset.Position(pos)
				out = append(out, finding{"any-error",
					fmt.Sprintf("%s:%d", path, p.Line), fn.Name.Name,
					"the error is never identified, so any failure satisfies it"})
			}
		}
		return false
	})
	return out
}

// successStatus reports whether an expression names a 2xx HTTP status.
func successStatus(e ast.Expr) bool {
	switch v := e.(type) {
	case *ast.SelectorExpr:
		pkg, _ := v.X.(*ast.Ident)
		if pkg == nil || pkg.Name != "http" {
			return false
		}
		switch v.Sel.Name {
		case "StatusOK", "StatusCreated", "StatusAccepted", "StatusNoContent",
			"StatusNonAuthoritativeInfo", "StatusResetContent", "StatusPartialContent":
			return true
		}
	case *ast.BasicLit:
		return len(v.Value) == 3 && v.Value[0] == '2'
	}
	return false
}

// identifyingFuncs returns the names of the package's functions that pin an
// error's identity — its code, its message, or its type — closed transitively
// over calls between them.
func identifyingFuncs(files map[string]*ast.File) map[string]bool {
	bodies := map[string]*ast.FuncDecl{}
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && fn.Body != nil {
				bodies[fn.Name.Name] = fn
			}
		}
	}
	identifying := map[string]bool{}
	for name, fn := range bodies {
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, _ := sel.X.(*ast.Ident)
			if pkg == nil {
				return true
			}
			switch {
			case pkg.Name == "errors" && (sel.Sel.Name == "As" || sel.Sel.Name == "Is"):
				identifying[name] = true
			case (pkg.Name == "assert" || pkg.Name == "require") &&
				(sel.Sel.Name == "ErrorContains" || sel.Sel.Name == "ErrorAs" ||
					sel.Sel.Name == "ErrorIs" || sel.Sel.Name == "EqualError" ||
					sel.Sel.Name == "Contains" || sel.Sel.Name == "Equal"):
				identifying[name] = true
			}
			return true
		})
	}
	for changed := true; changed; {
		changed = false
		for name, fn := range bodies {
			if identifying[name] {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok && callsAsserting(call, identifying) {
					identifying[name] = true
					changed = true
				}
				return true
			})
		}
	}
	return identifying
}
