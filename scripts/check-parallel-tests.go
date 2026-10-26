//go:build ignore

// check-parallel-tests holds the simulators' SDK and CLI suites to running in
// parallel. Each suite talks to one simulator process the way many clients
// talk to one cloud, and a test that runs alone leaves the job waiting on its
// sleeps, polls and CLI start-ups with nothing else in flight.
//
// Every top-level test in a suite starts with t.Parallel(), or names itself in
// the suite's serial-tests.txt with the reason it cannot share the simulator:
// it changes an account-, project- or subscription-wide setting other tests
// read, sets an environment variable, or restarts the simulator. A listed test
// that now starts with t.Parallel(), or that no longer exists, fails too, so
// the list only shrinks to what is true.
//
// Run from the repository root:
//
//	go run scripts/check-parallel-tests.go
package main

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var suites = []string{
	"simulator-aws/sdk-tests", "simulator-aws/cli-tests",
	"simulator-gcp/sdk-tests", "simulator-gcp/cli-tests",
	"simulator-azure/sdk-tests", "simulator-azure/cli-tests",
}

func main() {
	failed := false
	for _, suite := range suites {
		problems, err := checkSuite(suite)
		if err != nil {
			fmt.Fprintf(os.Stderr, "check-parallel-tests: %s: %v\n", suite, err)
			os.Exit(2)
		}
		for _, problem := range problems {
			fmt.Fprintf(os.Stderr, "%s: %s\n", suite, problem)
			failed = true
		}
	}
	if failed {
		fmt.Fprintln(os.Stderr, "check-parallel-tests: start each test with t.Parallel(), or list it in its suite's serial-tests.txt with the reason it must run alone")
		os.Exit(1)
	}
}

func checkSuite(dir string) ([]string, error) {
	serial, err := readSerial(filepath.Join(dir, "serial-tests.txt"))
	if err != nil {
		return nil, err
	}
	files, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no test files")
	}
	fset := token.NewFileSet()
	var parsedFiles []*ast.File
	for _, file := range files {
		parsed, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			return nil, err
		}
		parsedFiles = append(parsedFiles, parsed)
	}
	changesProcess := processChangers(parsedFiles)
	seen := map[string]bool{}
	var problems []string
	for _, parsed := range parsedFiles {
		for _, decl := range parsed.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "Test") || fn.Name.Name == "TestMain" || !takesT(fn) {
				continue
			}
			name := fn.Name.Name
			seen[name] = true
			parallel := startsParallel(fn)
			_, listed := serial[name]
			switch {
			case parallel && listed:
				problems = append(problems, fmt.Sprintf("%s starts with t.Parallel() and is listed as serial", name))
			case !parallel && !listed:
				problems = append(problems, fmt.Sprintf("%s (%s) neither starts with t.Parallel() nor is listed as serial",
					name, filepath.Base(fset.Position(fn.Pos()).Filename)))
			case parallel && changesProcess[name]:
				problems = append(problems, fmt.Sprintf("%s starts with t.Parallel() and reaches t.Setenv or t.Chdir, which change the whole test process", name))
			}
		}
	}
	var stale []string
	for name := range serial {
		if !seen[name] {
			stale = append(stale, name)
		}
	}
	sort.Strings(stale)
	for _, name := range stale {
		problems = append(problems, fmt.Sprintf("serial-tests.txt lists %s, which the suite does not define", name))
	}
	return problems, nil
}

// processChangers is the set of the suite's functions that call t.Setenv or
// t.Chdir, directly or through other functions of the suite.
func processChangers(files []*ast.File) map[string]bool {
	calls := map[string]map[string]bool{}
	changers := map[string]bool{}
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			name := fn.Name.Name
			if calls[name] == nil {
				calls[name] = map[string]bool{}
			}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch fun := call.Fun.(type) {
				case *ast.Ident:
					calls[name][fun.Name] = true
				case *ast.SelectorExpr:
					if fun.Sel.Name == "Setenv" || fun.Sel.Name == "Chdir" {
						if x, ok := fun.X.(*ast.Ident); ok && x.Name != "os" {
							changers[name] = true
						}
					}
				}
				return true
			})
		}
	}
	for grew := true; grew; {
		grew = false
		for name, callees := range calls {
			if changers[name] {
				continue
			}
			for callee := range callees {
				if changers[callee] {
					changers[name] = true
					grew = true
					break
				}
			}
		}
	}
	return changers
}

func takesT(fn *ast.FuncDecl) bool {
	params := fn.Type.Params.List
	if len(params) != 1 || len(params[0].Names) != 1 {
		return false
	}
	star, ok := params[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "T"
}

func startsParallel(fn *ast.FuncDecl) bool {
	if fn.Body == nil || len(fn.Body.List) == 0 {
		return false
	}
	stmt, ok := fn.Body.List[0].(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := stmt.X.(*ast.CallExpr)
	if !ok || len(call.Args) != 0 {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "Parallel"
}

// readSerial reads "TestName<TAB>reason" lines; a reason is required.
func readSerial(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	serial := map[string]string{}
	scanner := bufio.NewScanner(f)
	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		name, reason, ok := strings.Cut(text, "\t")
		if !ok || strings.TrimSpace(reason) == "" {
			return nil, fmt.Errorf("%s:%d: want TestName<TAB>reason, got %q", path, line, text)
		}
		if _, dup := serial[name]; dup {
			return nil, fmt.Errorf("%s:%d: %s is listed twice", path, line, name)
		}
		serial[name] = strings.TrimSpace(reason)
	}
	return serial, scanner.Err()
}
