//go:build ignore

// check-test-network-ranges holds every test that places a workload on a
// cloud network to a private range of its own within its suite. A suite's
// tests share one simulator and one container engine, and the engine carries
// two live networks over one range no better than two cables into one port:
// the second workload fails to boot with "address range held by another
// engine network". The cloud itself lets every VPC, VNet and subnetwork reuse
// 10.0.0.0/16, so nothing in a test's wording stops the reuse; this does.
//
// A function places a workload when it names an API or CLI verb that boots
// one onto a cloud network, or calls a function in its suite that does. Two
// placing functions of one suite whose RFC 1918 ranges overlap fail the check.
// A function that only reads or configures the control plane may share its
// range with anything.
//
// Run from the repository root; -fix renumbers the later function of each
// collision onto a second octet nothing in its suite uses:
//
//	go run scripts/check-test-network-ranges.go [-fix] [dir ...]
package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var suites = []string{
	"simulator-aws/sdk-tests", "simulator-aws/cli-tests",
	"simulator-gcp/sdk-tests", "simulator-gcp/cli-tests",
	"simulator-azure/sdk-tests", "simulator-azure/cli-tests",
}

// placers are the names a function reaches a placement through: the SDK
// operations and CLI verbs that boot a workload onto a VPC, a virtual network
// or a subnetwork, per the services that join one in each simulator.
var placers = []string{
	// Amazon EC2, Auto Scaling, Amazon ECS and AWS Lambda in a VPC.
	"RunInstances", "StartInstances", "CreateFleet", "RunTask", "StartTask", "CreateService", "UpdateService",
	"CreateAutoScalingGroup", "UpdateAutoScalingGroup", "SetDesiredCapacity", "StartInstanceRefresh",
	"CreateFunction", "UpdateFunctionConfiguration",
	"run-instances", "start-instances", "create-fleet", "run-task", "start-task", "create-service", "update-service",
	"create-auto-scaling-group", "update-auto-scaling-group", "set-desired-capacity", "start-instance-refresh",
	"create-function", "update-function-configuration",
	// Compute Engine instances and Cloud Run with VPC access.
	"Instances.Insert", "Instances.BulkInsert", "VpcAccess", "compute instances create", "--vpc-connector", "--network=",
	// Azure virtual machines, Container Apps environments and App Service in a virtual network.
	"NewVirtualMachinesClient", "VnetConfiguration", "VirtualNetworkSubnetID", "vm create", "containerapp env create",
	"vnet-integration", "--infrastructure-subnet-resource-id",
}

var cidr = regexp.MustCompile(`\b(10\.\d{1,3}\.\d{1,3}\.\d{1,3}|172\.(?:1[6-9]|2\d|3[01])\.\d{1,3}\.\d{1,3}|192\.168\.\d{1,3}\.\d{1,3})/(1[6-9]|2[0-9])\b`)

type function struct {
	name   string
	file   string
	pos    int
	end    int
	ranges []netip.Prefix
	calls  map[string]bool
	places bool
}

func main() {
	fix := flag.Bool("fix", false, "renumber the later function of each collision onto a free second octet")
	flag.Parse()
	dirs := flag.Args()
	if len(dirs) == 0 {
		dirs = suites
	}
	failed := false
	for _, dir := range dirs {
		collisions, err := check(dir, *fix)
		if err != nil {
			fmt.Fprintf(os.Stderr, "check-test-network-ranges: %s: %v\n", dir, err)
			os.Exit(2)
		}
		if len(collisions) > 0 {
			failed = true
			for _, c := range collisions {
				fmt.Printf("%s: %s\n", dir, c)
			}
		}
	}
	if failed {
		fmt.Println("check-test-network-ranges: two tests of one suite place workloads on overlapping ranges; give each its own (-fix renumbers)")
		os.Exit(1)
	}
	fmt.Println("check-test-network-ranges: every placing test holds a range of its own in its suite")
}

func check(dir string, fix bool) ([]string, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	sources := map[string][]byte{}
	var functions []*function
	for _, path := range paths {
		src, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		sources[path] = src
		f, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			return nil, err
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			entry := &function{name: fn.Name.Name, file: path, pos: fset.Position(fn.Pos()).Offset, end: fset.Position(fn.End()).Offset, calls: map[string]bool{}}
			text := string(src[entry.pos:entry.end])
			for _, p := range placers {
				if strings.Contains(text, p) {
					entry.places = true
				}
			}
			seen := map[netip.Prefix]bool{}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch v := n.(type) {
				case *ast.Ident:
					entry.calls[v.Name] = true
				case *ast.BasicLit:
					if v.Kind != token.STRING {
						return true
					}
					s, err := strconv.Unquote(v.Value)
					if err != nil {
						return true
					}
					for _, m := range cidr.FindAllString(s, -1) {
						if p, err := netip.ParsePrefix(m); err == nil && !seen[p.Masked()] {
							seen[p.Masked()] = true
							entry.ranges = append(entry.ranges, p.Masked())
						}
					}
				}
				return true
			})
			functions = append(functions, entry)
		}
	}
	if len(functions) == 0 {
		return nil, fmt.Errorf("no test function found: the walk did not reach the suite")
	}
	// A function places through a function of its suite that places.
	byName := map[string]*function{}
	for _, fn := range functions {
		byName[fn.name] = fn
	}
	for changed := true; changed; {
		changed = false
		for _, fn := range functions {
			if fn.places {
				continue
			}
			for call := range fn.calls {
				if callee, ok := byName[call]; ok && callee != fn && callee.places {
					fn.places, changed = true, true
					break
				}
			}
		}
	}
	placing := 0
	for _, fn := range functions {
		if fn.places {
			placing++
		}
	}
	if placing == 0 {
		return nil, fmt.Errorf("no function places a workload: the placer list does not match this suite")
	}
	sort.Slice(functions, func(i, j int) bool {
		if functions[i].file != functions[j].file {
			return functions[i].file < functions[j].file
		}
		return functions[i].pos < functions[j].pos
	})

	used := map[int]bool{}
	for _, fn := range functions {
		for _, p := range fn.ranges {
			if p.Addr().As4()[0] == 10 {
				used[int(p.Addr().As4()[1])] = true
			}
		}
	}
	var collisions []string
	reported := map[string]bool{}
	edits := map[string][]edit{}
	var claimed []*function
	for _, fn := range functions {
		if !fn.places || len(fn.ranges) == 0 {
			continue
		}
		renumber := map[int]int{}
		for _, p := range fn.ranges {
			for _, earlier := range claimed {
				for _, q := range earlier.ranges {
					if !p.Overlaps(q) {
						continue
					}
					pair := fn.name + " " + earlier.name
					if !reported[pair] {
						reported[pair] = true
						collisions = append(collisions, fmt.Sprintf("%s (%s) and %s (%s) both place workloads on %s and %s",
							fn.name, filepath.Base(fn.file), earlier.name, filepath.Base(earlier.file), p, q))
					}
					if fix && p.Addr().As4()[0] == 10 {
						octet := int(p.Addr().As4()[1])
						if _, done := renumber[octet]; !done {
							renumber[octet] = freeOctet(used)
						}
					}
				}
			}
		}
		for from, to := range renumber {
			edits[fn.file] = append(edits[fn.file], edit{pos: fn.pos, end: fn.end, from: from, to: to})
			for i, p := range fn.ranges {
				if p.Addr().As4()[0] == 10 && int(p.Addr().As4()[1]) == from {
					a := p.Addr().As4()
					a[1] = byte(to)
					fn.ranges[i] = netip.PrefixFrom(netip.AddrFrom4(a), p.Bits())
				}
			}
		}
		claimed = append(claimed, fn)
	}
	if fix {
		for path, list := range edits {
			src := sources[path]
			sort.Slice(list, func(i, j int) bool { return list[i].pos > list[j].pos })
			for _, e := range list {
				body := src[e.pos:e.end]
				body = regexp.MustCompile(`\b10\.`+strconv.Itoa(e.from)+`\.`).ReplaceAll(body, []byte("10."+strconv.Itoa(e.to)+"."))
				src = append(append(append([]byte{}, src[:e.pos]...), body...), src[e.end:]...)
			}
			if !bytes.Equal(src, sources[path]) {
				if err := os.WriteFile(path, src, 0o644); err != nil {
					return nil, err
				}
			}
		}
	}
	return collisions, nil
}

type edit struct {
	pos, end int
	from, to int
}

// freeOctet claims the highest second octet of 10.0.0.0/8 no range in the
// suite uses.
func freeOctet(used map[int]bool) int {
	for o := 255; o > 0; o-- {
		if !used[o] {
			used[o] = true
			return o
		}
	}
	panic("every second octet of 10.0.0.0/8 is in use")
}
