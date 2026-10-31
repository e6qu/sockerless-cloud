//go:build ignore

// gen-gcp-audit-methods compiles simulator-gcp/audit_methods_vendored.json from
// the vendored audit-logging pages under specs/cloud-api/gcp/: which methods a
// service audits, under which methodName, in which audit log category, and
// checking which permissions. The simulator audits a call of these services
// only when the page lists it, so nothing about a service's audit coverage is
// derived from a naming convention where Google states it.
//
// Most pages share one layout — an "API interface audit logs" section with a
// block per method. Resource Manager's lists its operations as Discovery-style
// identifiers under each audit log category instead, and is read that way.
//
// Run from the repository root: go run scripts/gen-gcp-audit-methods.go
package main

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

type source struct {
	File    string `json:"file"`
	Service string `json:"service"`
}

type permission struct {
	Permission string `json:"permission"`
	Type       string `json:"type"`
}

type method struct {
	Service     string       `json:"service"`
	Method      string       `json:"method"`
	LogType     string       `json:"logType"`
	Permissions []permission `json:"permissions"`
	LongRunning bool         `json:"longRunning"`
}

var sources = []source{
	{"run-audit-logging.md.gz", "run.googleapis.com"},
	{"bigquery-audit-logs.md.gz", "bigquery.googleapis.com"},
	{"sql-audit-logging.md.gz", "cloudsql.googleapis.com"},
	{"logging-audit-logging.md.gz", "logging.googleapis.com"},
	{"resource-manager-audit-logging.md.gz", "cloudresourcemanager.googleapis.com"},
	{"service-usage-audit-logging.md.gz", "serviceusage.googleapis.com"},
	{"vpcaccess-audit-logging.md.gz", "vpcaccess.googleapis.com"},
	{"api-gateway-audit-logging.md.gz", "apigateway.googleapis.com"},
	{"dataflow-audit-logging.md.gz", "dataflow.googleapis.com"},
	{"cloudquotas-audit-logging.md.gz", "cloudquotas.googleapis.com"},
}

var (
	tagPattern        = regexp.MustCompile(`<[^>]+>`)
	serviceNameLine   = regexp.MustCompile(`protoPayload\.serviceName="([^"]+)"`)
	methodLine        = regexp.MustCompile("^- \\*\\*Method\\*\\*: `([^`]+)`")
	logTypeLine       = regexp.MustCompile(`^- \*\*Audit log type\*\*: \[([^\]]+)\]`)
	permissionLine    = regexp.MustCompile("^  - `([A-Za-z0-9_.]+) - (ADMIN_READ|ADMIN_WRITE|DATA_READ|DATA_WRITE)`")
	longRunningLine   = regexp.MustCompile(`^- \*\*Method is a long-running or streaming operation\*\*: (.*)`)
	operationLine     = regexp.MustCompile("^- ` *(cloudresourcemanager\\.[a-z0-9]+\\.[A-Za-z]+\\.[A-Za-z]+) *`")
	categoryLine      = regexp.MustCompile(`^(Admin Activity|Data Access) logs \((ADMIN_WRITE|ADMIN_READ)([^)]*)\)`)
	auditedOperations = "## Audited operations"
)

func readPage(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	var lines []string
	scanner := bufio.NewScanner(zr)
	scanner.Buffer(make([]byte, 1<<20), 1<<24)
	for scanner.Scan() {
		lines = append(lines, strings.TrimRight(tagPattern.ReplaceAllString(scanner.Text(), ""), " "))
	}
	return lines, scanner.Err()
}

// dataAccessType is the Data Access log type a method's entry is gated by:
// the broadest data type among the permissions it checks.
func dataAccessType(permissions []permission) string {
	logType := "ADMIN_READ"
	for _, p := range permissions {
		switch {
		case p.Type == "DATA_WRITE":
			return "DATA_WRITE"
		case p.Type == "DATA_READ":
			logType = "DATA_READ"
		}
	}
	return logType
}

// methodBlocks reads the per-method blocks of a page's API interface section.
func methodBlocks(service string, lines []string) ([]method, error) {
	var methods []method
	var current *method
	var category string
	flush := func() error {
		if current == nil {
			return nil
		}
		switch category {
		case "Admin activity":
			current.LogType = "ADMIN_WRITE"
		case "Data access":
			current.LogType = dataAccessType(current.Permissions)
		default:
			return fmt.Errorf("%s: method %s declares audit log type %q", service, current.Method, category)
		}
		methods = append(methods, *current)
		current = nil
		return nil
	}
	for _, line := range lines {
		if m := methodLine.FindStringSubmatch(line); m != nil {
			if err := flush(); err != nil {
				return nil, err
			}
			current = &method{Service: service, Method: m[1]}
			category = ""
			continue
		}
		if current == nil {
			continue
		}
		if m := logTypeLine.FindStringSubmatch(line); m != nil {
			category = m[1]
		} else if m := permissionLine.FindStringSubmatch(line); m != nil {
			current.Permissions = append(current.Permissions, permission{m[1], m[2]})
		} else if m := longRunningLine.FindStringSubmatch(line); m != nil {
			current.LongRunning = strings.Contains(m[1], "Long-running") || strings.HasPrefix(m[1], "Yes")
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return methods, nil
}

// operationLists reads Resource Manager's audited operations, listed under
// each audit log category as versioned Discovery identifiers.
func operationLists(service string, lines []string) ([]method, error) {
	var methods []method
	section, logType := false, ""
	for _, line := range lines {
		switch {
		case line == auditedOperations:
			section = true
			continue
		case strings.HasPrefix(line, "## "):
			section = false
		}
		if !section {
			continue
		}
		if m := categoryLine.FindStringSubmatch(line); m != nil {
			// Operations audited under another service's name are that
			// service's entries, not Resource Manager's.
			logType = m[2]
			if m[3] != "" {
				logType = ""
			}
			continue
		}
		if m := operationLine.FindStringSubmatch(line); m != nil && logType != "" {
			methods = append(methods, method{Service: service, Method: m[1], LogType: logType})
		}
	}
	if len(methods) == 0 {
		return nil, fmt.Errorf("%s: no audited operations found", service)
	}
	return methods, nil
}

func main() {
	var methods []method
	for _, src := range sources {
		lines, err := readPage("specs/cloud-api/gcp/" + src.File)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		var declared string
		for _, line := range lines {
			if m := serviceNameLine.FindStringSubmatch(line); m != nil {
				declared = m[1]
				break
			}
		}
		if declared != "" && declared != src.Service {
			fmt.Fprintf(os.Stderr, "%s declares service %s, expected %s\n", src.File, declared, src.Service)
			os.Exit(1)
		}
		var found []method
		if src.Service == "cloudresourcemanager.googleapis.com" {
			found, err = operationLists(src.Service, lines)
		} else {
			found, err = methodBlocks(src.Service, lines)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if len(found) == 0 {
			fmt.Fprintf(os.Stderr, "%s lists no audited methods; the page layout changed\n", src.File)
			os.Exit(1)
		}
		methods = append(methods, found...)
	}
	// A page repeats a method under each interface it belongs to; keep one
	// row per service and method, the first listed.
	seen := map[string]bool{}
	var unique []method
	for _, m := range methods {
		key := m.Service + " " + m.Method
		if seen[key] {
			continue
		}
		seen[key] = true
		if m.Permissions == nil {
			m.Permissions = []permission{}
		}
		unique = append(unique, m)
	}
	sort.Slice(unique, func(i, j int) bool {
		if unique[i].Service != unique[j].Service {
			return unique[i].Service < unique[j].Service
		}
		return unique[i].Method < unique[j].Method
	})
	payload := struct {
		Sources   []source `json:"sources"`
		Retrieved string   `json:"retrieved"`
		Note      string   `json:"note"`
		Methods   []method `json:"methods"`
	}{
		Sources:   sources,
		Retrieved: time.Now().UTC().Format("2006-01-02"),
		Note: "Each service's audited methods, read from its audit-logging page vendored under specs/cloud-api/gcp/ " +
			"by scripts/gen-gcp-audit-methods.go. logType is the audit log category the page states: ADMIN_WRITE for " +
			"Admin Activity, and for Data Access the broadest data type among the permissions the method checks. " +
			"Resource Manager lists its operations as versioned identifiers without permissions.",
		Methods: unique,
	}
	out, err := json.MarshalIndent(payload, "", " ")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.WriteFile("simulator-gcp/audit_methods_vendored.json", append(out, '\n'), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("%d audited methods across %d services -> simulator-gcp/audit_methods_vendored.json\n", len(unique), len(sources))
}
