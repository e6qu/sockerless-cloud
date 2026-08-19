//go:build ignore

// check-lock-pairing reports read-write locks whose acquire and release do not
// match — an RLock released with Unlock, or a Lock released with RUnlock. Run
// from the repository root:
//
//	go run scripts/check-lock-pairing.go <dir>...
//
// This exists because sync.RWMutex answers a mismatch with `fatal error: sync:
// Unlock of unlocked RWMutex`, which is not a panic a handler can recover: it
// takes the whole process down. In a simulator that means the crash is not one
// failing test but every test after it, all reporting `connection refused` at a
// port nothing is listening on any more — a failure whose symptom points
// nowhere near its cause. That is exactly how it arrived: converting a service
// to read locks changed an acquire from Lock to RLock and left the matching
// Unlock behind, and the first request to reach that handler killed the
// simulator.
//
// Neither the compiler nor `go vet` sees it, because both calls are valid
// methods on the same valid receiver. Only executing the path finds it, so a
// handler no test exercises can carry the defect indefinitely. The syntax tree,
// though, shows it plainly.
//
// The detector reports a function when, for one lock, it takes a read lock and
// calls Unlock without ever taking the write lock — the Unlock has nothing to
// pair with — or takes the write lock and calls RUnlock without ever taking the
// read lock. A function that legitimately does both (releasing a read lock to
// take the write lock) has all four calls and is not reported, so the check
// stays silent on the upgrade pattern instead of pushing people to suppress it.
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type lockUse struct {
	rlock, lock, runlock, unlock int
	firstBad                     token.Pos
}

type finding struct {
	pos      string
	fn       string
	receiver string
	problem  string
}

func main() {
	roots := os.Args[1:]
	if len(roots) == 0 {
		roots = []string{"."}
	}
	fset := token.NewFileSet()
	var paths []string
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
			if strings.HasSuffix(path, ".go") {
				paths = append(paths, path)
			}
			return nil
		})
	}
	sort.Strings(paths)

	var findings []finding
	for _, path := range paths {
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			continue
		}
		findings = append(findings, scan(fset, file, path)...)
	}
	sort.Slice(findings, func(i, j int) bool { return findings[i].pos < findings[j].pos })
	for _, f := range findings {
		fmt.Printf("lock-pairing %s\t%s\t%s\t%s\n", f.pos, f.fn, f.receiver, f.problem)
	}
	fmt.Fprintf(os.Stderr, "lock-pairing findings: %d\n", len(findings))
}

func scan(fset *token.FileSet, file *ast.File, path string) []finding {
	var out []finding
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		// Function literals carry their own acquire/release pairs — a
		// goroutine body or a deferred closure locks and unlocks entirely
		// within itself. Attributing their calls to the enclosing function
		// would merge two independent pairings into one tally and invent a
		// mismatch out of two correct halves.
		for _, use := range usesByReceiver(fn.Body) {
			out = append(out, use.findings(fset, path, fn.Name.Name)...)
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			lit, ok := n.(*ast.FuncLit)
			if !ok {
				return true
			}
			for _, use := range usesByReceiver(lit.Body) {
				out = append(out, use.findings(fset, path, fn.Name.Name+".func")...)
			}
			return true
		})
	}
	return out
}

type receiverUse struct {
	name string
	use  lockUse
}

// usesByReceiver tallies the lock calls in one body, keyed by the expression
// naming the lock, and skipping any nested function literal, whose calls belong
// to that literal rather than to the body containing it.
func usesByReceiver(body *ast.BlockStmt) []receiverUse {
	uses := map[string]*lockUse{}
	var visit func(n ast.Node) bool
	visit = func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) != 0 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		var field *int
		var bad bool
		key := exprString(sel.X)
		if key == "" {
			return true
		}
		use, ok := uses[key]
		if !ok {
			use = &lockUse{}
			uses[key] = use
		}
		switch sel.Sel.Name {
		case "RLock":
			field = &use.rlock
		case "Lock":
			field = &use.lock
		case "RUnlock":
			field, bad = &use.runlock, true
		case "Unlock":
			field, bad = &use.unlock, true
		default:
			return true
		}
		*field++
		if bad && use.firstBad == token.NoPos {
			use.firstBad = call.Pos()
		}
		return true
	}
	ast.Inspect(body, visit)

	names := make([]string, 0, len(uses))
	for name := range uses {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]receiverUse, 0, len(names))
	for _, name := range names {
		out = append(out, receiverUse{name: name, use: *uses[name]})
	}
	return out
}

func (r receiverUse) findings(fset *token.FileSet, path, fn string) []finding {
	u := r.use
	var problem string
	switch {
	case u.rlock > 0 && u.unlock > 0 && u.lock == 0:
		problem = "RLock released with Unlock — sync fatals and takes the process down; use RUnlock"
	case u.lock > 0 && u.runlock > 0 && u.rlock == 0:
		problem = "Lock released with RUnlock — sync fatals and takes the process down; use Unlock"
	default:
		return nil
	}
	pos := fset.Position(u.firstBad)
	return []finding{{
		pos:      fmt.Sprintf("%s:%d", path, pos.Line),
		fn:       fn,
		receiver: r.name,
		problem:  problem,
	}}
}

// exprString renders the expression a lock method is called on, so `s.mu` and
// `lambdaDurableMu` are distinct keys and the same lock in two places is one.
func exprString(expr ast.Expr) string {
	var sb strings.Builder
	if err := printer.Fprint(&sb, token.NewFileSet(), expr); err != nil {
		return ""
	}
	return sb.String()
}
