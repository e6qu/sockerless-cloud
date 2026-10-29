//go:build ignore

// gen-gcp-iam-catalog compiles simulator-gcp/iam_catalog_vendored.json from
// the IAM pages vendored under specs/cloud-api/gcp/:
//
//   - the predefined roles and their permissions, and every permission each
//     service defines, from the iam-roles-<service> pages;
//   - the permissions each Cloud Storage JSON method and each Pub/Sub method
//     requires, from the services' own method tables;
//   - the Cloud Storage permissions the basic roles hold intrinsically;
//   - the service agents Google creates in a project and the role it grants
//     each on the project.
//
// Run from the repository root: go run scripts/gen-gcp-iam-catalog.go
package main

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
)

const specDir = "specs/cloud-api/gcp/"

// rolePages are the services whose roles and permissions the simulator
// evaluates: every service it serves.
var rolePages = []string{
	"apigateway", "artifactregistry", "bigquery", "bigtable", "billing", "cloudbuild",
	"cloudfunctions", "cloudkms", "cloudscheduler", "cloudsql", "cloudtasks", "compute",
	"dataflow", "dns", "eventarc", "firestore", "iam", "logging", "pubsub", "redis",
	"resourcemanager", "run", "secretmanager", "serviceusage", "spanner", "storage", "vpcaccess",
}

type role struct {
	Name                string   `json:"name"`
	Title               string   `json:"title"`
	Description         string   `json:"description"`
	Stage               string   `json:"stage"`
	IncludedPermissions []string `json:"includedPermissions"`
}

type requirement struct {
	Permission string `json:"permission"`
	// On names what the permission is checked on when it is not the
	// resource the method addresses: "project", "source", "destination", a
	// request field ("field:topic"), or the topic a subscription is attached
	// to ("attached:topic").
	On       string `json:"on,omitempty"`
	Optional bool   `json:"optional,omitempty"`
}

type serviceAgent struct {
	Service string `json:"service"`
	// Email spells the agent with PROJECT_NUMBER where the project's number
	// goes.
	Email string `json:"email"`
	Role  string `json:"role"`
}

var (
	roleRow        = regexp.MustCompile(`(?s)<td class="role-description"><h4 id="([^"]+)" class="role-title[^>]*>(.*?)</h4>(.*?)</td>\s*<td class="role-permissions">(.*?)</td>`)
	permissionHead = regexp.MustCompile(`<h4 id="([^"]+)" class="permission-name`)
	codeSpan       = regexp.MustCompile(`<code[^>]*>([^<]+)</code>`)
	wildcardSpan   = regexp.MustCompile(`(?s)<span class="iamperm-wildcard">.*?</span>`)
	paragraph      = regexp.MustCompile(`(?s)<p>(.*?)</p>`)
	tags           = regexp.MustCompile(`<[^>]+>`)
	tableRow       = regexp.MustCompile(`(?s)<tr class="(?:odd|even)">(.*?)</tr>`)
	tableCell      = regexp.MustCompile(`(?s)<td[^>]*>(.*?)</td>`)
	backticked     = regexp.MustCompile("`([a-zA-Z]+\\.[a-zA-Z.]+)`")
	markdownLink   = regexp.MustCompile("\\[`([a-zA-Z.]+)`\\]")
	agentRow       = regexp.MustCompile(`(?s)<td><h4 id="[^"]+" class="service-agent-name.*?</tr>`)
	agentService   = regexp.MustCompile(`(?s)agent</a>\s*for\s*<code[^>]*>([a-z0-9.-]+\.googleapis\.com)</code>|Service agent for <code[^>]*>([a-z0-9.-]+\.googleapis\.com)</code>`)
	agentEmail     = regexp.MustCompile(`<p><code[^>]*>service-</code><var[^>]*>PROJECT_NUMBER</var><code[^>]*>(@[a-z0-9.-]+)</code></p>`)
	agentRole      = regexp.MustCompile(`\(<code[^>]*>(roles/[A-Za-z0-9._]+)</code>\)</p>\s*<p>Granted on the project\.</p>`)
)

func readPage(name string) string {
	f, err := os.Open(specDir + name)
	check(err)
	defer f.Close()
	zr, err := gzip.NewReader(f)
	check(err)
	raw, err := io.ReadAll(zr)
	check(err)
	return string(raw)
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func text(fragment string) string {
	return strings.Join(strings.Fields(html.UnescapeString(tags.ReplaceAllString(fragment, " "))), " ")
}

// rolesOf reads a roles-and-permissions page: each role's permissions, with a
// wildcard heading dropped in favour of the permissions listed under it.
func rolesOf(page string) ([]role, []string) {
	var roles []role
	for _, m := range roleRow.FindAllStringSubmatch(page, -1) {
		r := role{Name: "roles/" + m[1], Title: text(m[2]), Stage: "GA"}
		if strings.Contains(m[2], "<sup>") {
			sup := regexp.MustCompile(`<sup>([^<]+)</sup>`).FindStringSubmatch(m[2])
			r.Stage = strings.ToUpper(strings.TrimSpace(sup[1]))
			r.Title = strings.TrimSpace(strings.TrimSuffix(r.Title, sup[1]))
		}
		for _, p := range paragraph.FindAllStringSubmatch(m[3], -1) {
			t := text(p[1])
			if t == "" || strings.HasPrefix(t, "(") || strings.HasPrefix(t, "Lowest-level resources") {
				continue
			}
			r.Description = t
			break
		}
		permissions := wildcardSpan.ReplaceAllString(m[4], "")
		for _, c := range codeSpan.FindAllStringSubmatch(permissions, -1) {
			r.IncludedPermissions = append(r.IncludedPermissions, permissionName(strings.TrimSpace(c[1])))
		}
		sort.Strings(r.IncludedPermissions)
		roles = append(roles, r)
	}
	var permissions []string
	for _, m := range permissionHead.FindAllStringSubmatch(page, -1) {
		permissions = append(permissions, permissionName(m[1]))
	}
	return roles, permissions
}

// serviceQualified is the spelling some pages give a permission,
// iam.googleapis.com/workforcePools.list, with the slash an underscore in a
// heading's id.
var serviceQualified = regexp.MustCompile(`^([a-z]+)\.googleapis\.com[/_](.+)$`)

// permissionName is a permission as APIs check it and policies name it: the
// page's wildcard headings and IAM's refusals spell iam.googleapis.com/x.verb
// as iam.x.verb.
func permissionName(p string) string {
	return serviceQualified.ReplaceAllString(p, "$1.$2")
}

// storageMethods reads Cloud Storage's "IAM permissions for JSON methods"
// tables. A permission marked with a footnote applies only in the case the
// footnote names, so it is not one every call of the method requires.
func storageMethods(page string, discovery map[string]bool) map[string][]requirement {
	out := map[string][]requirement{}
	for _, row := range tableRow.FindAllStringSubmatch(page, -1) {
		cells := tableCell.FindAllStringSubmatch(row[1], -1)
		if len(cells) != 3 {
			continue
		}
		resource, method := text(cells[0][1]), text(cells[1][1])
		parts := strings.Split(resource, ".")
		for i, p := range parts {
			parts[i] = strings.ToLower(p[:1]) + p[1:]
		}
		collection := strings.Join(parts, ".")
		id := ""
		for _, candidate := range []string{collection, collection + "s"} {
			if discovery["storage."+candidate+"."+method] {
				id = "storage." + candidate + "." + method
			}
		}
		if id == "" {
			fmt.Fprintf(os.Stderr, "storage: %s %s names no Discovery method; skipped\n", resource, method)
			continue
		}
		var reqs []requirement
		for _, line := range strings.Split(cells[2][1], "<br />") {
			c := codeSpan.FindStringSubmatch(line)
			if c == nil || strings.Contains(line, "<sup>") {
				continue
			}
			r := requirement{Permission: c[1]}
			switch t := text(line); {
			case strings.Contains(t, "(for the source"):
				r.On = "source"
			case strings.Contains(t, "(for the destination"):
				r.On = "destination"
			}
			reqs = append(reqs, r)
		}
		out[id] = reqs
	}
	return out
}

// pubsubMethods reads Pub/Sub's "Required permissions" tables, whose third
// column states each permission and what it is checked on.
func pubsubMethods(page string) map[string][]requirement {
	out := map[string][]requirement{}
	for _, line := range strings.Split(page, "\n") {
		if !strings.HasPrefix(line, "| [`projects.") {
			continue
		}
		cols := strings.Split(line, "|")
		if len(cols) < 4 {
			continue
		}
		m := markdownLink.FindStringSubmatch(cols[1])
		if m == nil {
			continue
		}
		method := m[1]
		segments := strings.Split(method, ".")
		own := strings.TrimSuffix(segments[len(segments)-2], "s")
		required := strings.TrimSpace(cols[3])
		var reqs []requirement
		matches := backticked.FindAllStringSubmatchIndex(required, -1)
		for i, idx := range matches {
			end := len(required)
			if i+1 < len(matches) {
				end = matches[i+1][0]
			}
			permission := required[idx[2]:idx[3]]
			target := required[idx[1]:end]
			r := requirement{Permission: permission}
			switch {
			case strings.Contains(target, "Cloud project"):
				r.On = "project"
			case strings.Contains(target, "topic that the subscription is attached to"):
				r.On = "attached:topic"
			case strings.Contains(target, "source subscription"):
				r.On = "field:subscription"
			default:
				for _, kind := range []string{"topic", "subscription", "schema", "snapshot"} {
					if strings.Contains(target, "requested "+kind) && kind != own {
						r.On = "field:" + kind
					}
				}
				if strings.Contains(target, "if any") {
					r.On = "field:" + strings.TrimSuffix(strings.Split(permission, ".")[1], "s")
					r.Optional = true
				}
			}
			reqs = append(reqs, r)
		}
		if strings.HasPrefix(required, "None") {
			reqs = []requirement{}
		}
		out["pubsub."+method] = reqs
	}
	return out
}

// storageBasicRoles reads the Cloud Storage permissions each basic role
// carries intrinsically, from the storage-iam-roles page.
func storageBasicRoles(page string) map[string][]string {
	out := map[string][]string{}
	start := strings.Index(page, "### Intrinsic permissions")
	end := strings.Index(page, "### Modifiable behavior")
	if start < 0 || end < start {
		check(fmt.Errorf("storage-iam-roles: no intrinsic permissions table"))
	}
	for _, row := range tableRow.FindAllStringSubmatch(page[start:end], -1) {
		cells := tableCell.FindAllStringSubmatch(row[1], -1)
		if len(cells) != 3 {
			continue
		}
		name := regexp.MustCompile(`roles/[a-z]+`).FindString(text(cells[0][1]))
		for _, c := range codeSpan.FindAllStringSubmatch(cells[2][1], -1) {
			out[name] = append(out[name], c[1])
		}
	}
	if len(out) != 3 {
		check(fmt.Errorf("storage-iam-roles: read %d basic roles, want 3", len(out)))
	}
	return out
}

func serviceAgents(page string) []serviceAgent {
	var out []serviceAgent
	for _, row := range agentRow.FindAllString(page, -1) {
		s := agentService.FindStringSubmatch(row)
		e := agentEmail.FindStringSubmatch(row)
		r := agentRole.FindStringSubmatch(row)
		if s == nil || e == nil || r == nil {
			continue
		}
		service := s[1]
		if service == "" {
			service = s[2]
		}
		out = append(out, serviceAgent{Service: service, Email: "service-PROJECT_NUMBER" + e[1], Role: r[1]})
	}
	return out
}

func storageDiscoveryMethods() map[string]bool {
	f, err := os.Open(specDir + "storage-v1.discovery.json.gz")
	check(err)
	defer f.Close()
	zr, err := gzip.NewReader(f)
	check(err)
	type resource struct {
		Methods   map[string]struct{ ID string } `json:"methods"`
		Resources map[string]resource            `json:"resources"`
	}
	var doc struct {
		Resources map[string]resource `json:"resources"`
	}
	check(json.NewDecoder(zr).Decode(&doc))
	ids := map[string]bool{}
	var walk func(map[string]resource)
	walk = func(rs map[string]resource) {
		for _, r := range rs {
			for _, m := range r.Methods {
				ids[m.ID] = true
			}
			walk(r.Resources)
		}
	}
	walk(doc.Resources)
	return ids
}

func main() {
	byName := map[string]role{}
	permissionSet := map[string]bool{}
	var sources []string
	for _, service := range rolePages {
		file := "iam-roles-" + service + ".md.gz"
		sources = append(sources, file)
		roles, permissions := rolesOf(readPage(file))
		if len(roles) == 0 || len(permissions) == 0 {
			check(fmt.Errorf("%s: read %d roles and %d permissions; the page layout changed", file, len(roles), len(permissions)))
		}
		for _, r := range roles {
			byName[r.Name] = r
		}
		for _, p := range permissions {
			permissionSet[p] = true
		}
	}
	var roles []role
	for _, r := range byName {
		roles = append(roles, r)
	}
	sort.Slice(roles, func(i, j int) bool { return roles[i].Name < roles[j].Name })
	var permissions []string
	for p := range permissionSet {
		permissions = append(permissions, p)
	}
	sort.Strings(permissions)

	methods := storageMethods(readPage("storage-iam-json.md.gz"), storageDiscoveryMethods())
	for id, reqs := range pubsubMethods(readPage("pubsub-access-control.md.gz")) {
		methods[id] = reqs
	}
	agents := serviceAgents(readPage("iam-service-agents.md.gz"))
	sort.Slice(agents, func(i, j int) bool { return agents[i].Email < agents[j].Email })
	sources = append(sources, "storage-iam-json.md.gz", "pubsub-access-control.md.gz", "storage-iam-roles.md.gz", "iam-service-agents.md.gz")

	payload := struct {
		Sources           []string                 `json:"sources"`
		Note              string                   `json:"note"`
		Roles             []role                   `json:"roles"`
		Permissions       []string                 `json:"permissions"`
		Methods           map[string][]requirement `json:"methods"`
		StorageBasicRoles map[string][]string      `json:"storageBasicRoles"`
		ServiceAgents     []serviceAgent           `json:"serviceAgents"`
	}{
		Sources: sources,
		Note: "Compiled by scripts/gen-gcp-iam-catalog.go from the pages under specs/cloud-api/gcp/: predefined roles and " +
			"the permissions each served service defines, the permissions Cloud Storage's JSON methods and Pub/Sub's methods " +
			"require (keyed by Discovery method ID), the Cloud Storage permissions basic roles hold intrinsically, and the " +
			"service agents granted a role on their project.",
		Roles:             roles,
		Permissions:       permissions,
		Methods:           methods,
		StorageBasicRoles: storageBasicRoles(readPage("storage-iam-roles.md.gz")),
		ServiceAgents:     agents,
	}
	out, err := json.MarshalIndent(payload, "", " ")
	check(err)
	check(os.WriteFile("simulator-gcp/iam_catalog_vendored.json", append(out, '\n'), 0o644))
	fmt.Printf("%d roles, %d permissions, %d method rows, %d service agents -> simulator-gcp/iam_catalog_vendored.json\n",
		len(roles), len(permissions), len(methods), len(agents))
}
