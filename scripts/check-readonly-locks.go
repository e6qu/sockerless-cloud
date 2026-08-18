//go:build ignore

// check-readonly-locks reports critical sections that hold an exclusive lock
// while only reading. Run from the repository root:
//
//	go run scripts/check-readonly-locks.go <dir>...
//
// This exists because the same defect arrived three times from one service in
// two days — a Query that read the whole table under a per-item lock, a Scan
// that copied under one acquisition per item, and finally every single-item
// read queueing behind every other operation because the lock guarding the
// item store was exclusive. Each was found by a user watching a page time out,
// and each was reported as its own bug. They are one shape: a process-wide
// lock taken for reading, so the service's read concurrency is one.
//
// The detector decides from the syntax tree. A function is reported when it
// takes `X.Lock()` and its critical section writes nothing: no call to a
// mutating store method, no assignment reaching outside the section, no
// channel operation or goroutine, and no call the detector cannot see into. A
// section that only reads under an exclusive lock either wants RLock, or wants
// a comment saying which later write it is being atomic with.
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

// mutatingMethods are the methods that write state a lock could be guarding. A
// critical section calling any of them is doing more than reading.
//
// Writing an HTTP response is deliberately absent. Including Write and Flush
// made every request handler a writer, which silently emptied the largest true
// cluster this detector had found — a dozen read-only Glue handlers — because
// they all answer with a response body. What matters is whether the section
// mutates the state the lock guards, not whether it eventually says something
// to the caller.
var mutatingMethods = map[string]bool{
	"Put": true, "Delete": true, "Update": true, "Upsert": true, "Set": true,
	"Add": true, "Store": true, "Remove": true, "Clear": true, "Reset": true,
	"Insert": true, "Swap": true, "CompareAndSwap": true,
	"Inc": true, "Dec": true,
}

type finding struct {
	pos  string
	fn   string
	lock string
}

func main() {
	roots := os.Args[1:]
	if len(roots) == 0 {
		roots = []string{"."}
	}
	fset := token.NewFileSet()
	var findings []finding
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
	for _, dir := range dirs {
		files := map[string]*ast.File{}
		for _, path := range byDir[dir] {
			if file, perr := parser.ParseFile(fset, path, nil, 0); perr == nil {
				files[path] = file
			}
		}
		// A section that calls a function which writes is writing, however
		// many calls deep. Without this the detector reported functions whose
		// whole purpose is mutation — configuring a host route, reapplying a
		// security group, publishing a version — as read-only, because the
		// write was one call away.
		writers := writingFuncs(files)
		paths := make([]string, 0, len(files))
		for path := range files {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		for _, path := range paths {
			findings = append(findings, scan(fset, files[path], path, writers)...)
		}
	}
	sort.Slice(findings, func(i, j int) bool { return findings[i].pos < findings[j].pos })
	for _, f := range findings {
		fmt.Printf("readonly-lock %s\t%s\t%s\n", f.pos, f.fn, f.lock)
	}
	fmt.Fprintf(os.Stderr, "readonly-lock findings: %d\n", len(findings))
}

func scan(fset *token.FileSet, file *ast.File, path string, writers map[string]bool) []finding {
	var out []finding
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		lock, at := exclusiveLock(fn.Body)
		if lock == "" {
			continue
		}
		if writesSomething(fn.Body, writers) {
			continue
		}
		// Only sections that read a store are reported. A lock held over a
		// small bookkeeping map costs a handful of nanoseconds and reporting
		// it would bury the class this exists for: a service's data plane
		// serialised behind one lock, which is what made single-item reads
		// take thirteen seconds.
		if !readsAStore(fn.Body) {
			continue
		}
		pos := fset.Position(at)
		out = append(out, finding{
			pos:  fmt.Sprintf("%s:%d", path, pos.Line),
			fn:   fn.Name.Name,
			lock: lock + " held for a critical section that only reads",
		})
	}
	return out
}

// exclusiveLock reports the name of the lock a function takes exclusively, and
// where. Only the `X.Lock()` form is considered: a read lock is the thing this
// detector is asking for, not against.
func exclusiveLock(body *ast.BlockStmt) (string, token.Pos) {
	name, pos := "", token.NoPos
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) != 0 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Lock" {
			return true
		}
		receiver, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if name == "" {
			name, pos = receiver.Name, call.Pos()
		}
		return true
	})
	return name, pos
}

// writesSomething reports whether the body does anything but read. It is
// deliberately generous: anything the detector cannot prove is a read counts
// as a write, so a report is worth reading rather than worth suppressing.
func writesSomething(body *ast.BlockStmt, writers map[string]bool) bool {
	declared := map[string]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		if assign, ok := n.(*ast.AssignStmt); ok && assign.Tok == token.DEFINE {
			for _, lhs := range assign.Lhs {
				if id, ok := lhs.(*ast.Ident); ok {
					declared[id.Name] = true
				}
			}
		}
		if decl, ok := n.(*ast.DeclStmt); ok {
			if gen, ok := decl.Decl.(*ast.GenDecl); ok {
				for _, spec := range gen.Specs {
					if value, ok := spec.(*ast.ValueSpec); ok {
						for _, id := range value.Names {
							declared[id.Name] = true
						}
					}
				}
			}
		}
		return true
	})

	writes := false
	ast.Inspect(body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.GoStmt, *ast.SendStmt:
			writes = true
		case *ast.UnaryExpr:
			if node.Op == token.ARROW {
				writes = true
			}
		case *ast.IncDecStmt:
			if !localTarget(node.X, declared) {
				writes = true
			}
		case *ast.AssignStmt:
			if node.Tok == token.DEFINE {
				return true
			}
			for _, lhs := range node.Lhs {
				if !localTarget(lhs, declared) {
					writes = true
				}
			}
		case *ast.CallExpr:
			if sel, ok := node.Fun.(*ast.SelectorExpr); ok {
				// A call on the response writer mutates the response, not the
				// state the lock guards. Counting w.Header().Set as a write
				// excluded every request handler that answers with a header —
				// which is all of them — and emptied the detector of exactly
				// the read paths it exists to find.
				if !onResponseWriter(sel.X) &&
					(mutatingMethods[sel.Sel.Name] || writers[sel.Sel.Name]) {
					writes = true
				}
			}
			// The builtins that mutate are calls to a plain identifier, not to
			// a method, and missing them made the detector report functions
			// whose whole job is removal — "unregister", "cancel" — as
			// read-only on its first run.
			if id, ok := node.Fun.(*ast.Ident); ok {
				switch id.Name {
				case "delete", "clear", "copy", "panic":
					writes = true
				}
				if writers[id.Name] {
					writes = true
				}
			}
		}
		return true
	})
	return writes
}

// localTarget reports whether an assignment target is a variable the section
// declared itself, or an element of one — writing to those escapes nothing.
func localTarget(expr ast.Expr, declared map[string]bool) bool {
	switch target := expr.(type) {
	case *ast.Ident:
		return declared[target.Name] || target.Name == "_"
	case *ast.IndexExpr:
		return localTarget(target.X, declared)
	case *ast.SelectorExpr:
		return localTarget(target.X, declared)
	case *ast.StarExpr:
		return localTarget(target.X, declared)
	}
	return false
}

// readsAStore reports whether the critical section reads through a store —
// Get, List or Filter — which is the shape a request handler's read path has.
func readsAStore(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "Get", "List", "Filter", "Len":
			found = true
		}
		return true
	})
	return found
}

// writingFuncs returns the names of the package's functions that write, closed
// transitively over calls between them.
func writingFuncs(files map[string]*ast.File) map[string]bool {
	bodies := map[string]*ast.FuncDecl{}
	for _, file := range files {
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
				bodies[fn.Name.Name] = fn
			}
		}
	}
	writers := map[string]bool{}
	for name, fn := range bodies {
		if writesSomething(fn.Body, nil) {
			writers[name] = true
		}
	}
	for changed := true; changed; {
		changed = false
		for name, fn := range bodies {
			if writers[name] {
				continue
			}
			if writesSomething(fn.Body, writers) {
				writers[name] = true
				changed = true
			}
		}
	}
	return writers
}

// onResponseWriter reports whether an expression is rooted at the HTTP response
// writer, by the name the handlers in this repository give it.
func onResponseWriter(expr ast.Expr) bool {
	for {
		switch node := expr.(type) {
		case *ast.Ident:
			return node.Name == "w" || node.Name == "rw"
		case *ast.SelectorExpr:
			expr = node.X
		case *ast.CallExpr:
			expr = node.Fun
		case *ast.IndexExpr:
			expr = node.X
		default:
			return false
		}
	}
}
