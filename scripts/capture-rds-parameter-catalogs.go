//go:build ignore

// capture-rds-parameter-catalogs writes
// simulator-aws/rds_parameter_catalogs_vendored.json: each DB parameter group
// family's parameters as the engine the simulator runs for that family
// reports them.
//
// The program starts each family's engine image with the arguments the
// simulator starts it with (rdsLoggingEngine and rdsPluginArgs), and reads the
// engine's own catalog of its settings:
//
//   - PostgreSQL: pg_settings, whose context separates the settings a restart
//     applies (postmaster, internal) from those a reload applies.
//   - MariaDB: information_schema.SYSTEM_VARIABLES, which states each
//     variable's type, range, values, read-only flag and command-line option.
//   - MySQL 8.0: performance_schema.global_variables and variables_info for
//     values and ranges, `mysqld --verbose --help` for the command-line
//     options and their descriptions, and a `SET GLOBAL v = @@GLOBAL.v` of
//     every variable, which the server refuses with error 1238 for a variable
//     it cannot change at runtime.
//
// A DB parameter group sets the engine's configuration, so a parameter is
// modifiable when the server takes it as a configuration option: every
// PostgreSQL setting but the internal ones, and each MySQL-family variable
// with a command-line option.
//
// It records each image, its image ID and the engine version it reported, so
// the catalog never carries a value the engine did not state.
//
// Run from the repository root with Docker available:
//
//	go run scripts/capture-rds-parameter-catalogs.go
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type family struct {
	Family string
	Image  string
	Kind   string
	Args   []string
}

// families pins the image of each family's newest version in
// simulator-aws/rds_engine_versions.go; TestRDSParameterCatalogImages fails
// when the two drift.
var families = []family{
	{"postgres16", "public.ecr.aws/docker/library/postgres:16.15-alpine", "postgres", postgresArgs},
	{"postgres17", "public.ecr.aws/docker/library/postgres:17.11-alpine", "postgres", postgresArgs},
	{"mysql8.0", "public.ecr.aws/docker/library/mysql:8.0.46", "mysql",
		[]string{"--default-authentication-plugin=mysql_native_password", "--binlog-expire-logs-seconds=0",
			"--plugin-dir=/var/lib/mysql/.sockerless-plugin"}},
	{"mariadb11.4", "public.ecr.aws/docker/library/mariadb:11.4.13", "mariadb",
		[]string{"--log-bin=binlog", "--binlog-expire-logs-seconds=0",
			"--plugin-dir=/var/lib/mysql/.sockerless-plugin", "--ignore-db-dirs=.sockerless-plugin"}},
}

var postgresArgs = []string{"-c", "archive_mode=on",
	"-c", "archive_command=mkdir -p sockerless_wal_archive && test ! -f sockerless_wal_archive/%f && cp %p sockerless_wal_archive/%f"}

type parameter struct {
	Name          string `json:"name"`
	Value         string `json:"value,omitempty"`
	ApplyType     string `json:"applyType"`
	DataType      string `json:"dataType"`
	AllowedValues string `json:"allowedValues,omitempty"`
	Modifiable    bool   `json:"isModifiable"`
	Description   string `json:"description,omitempty"`
}

type catalog struct {
	Image         string      `json:"image"`
	ImageID       string      `json:"imageId"`
	EngineVersion string      `json:"engineVersion"`
	Source        string      `json:"source"`
	Parameters    []parameter `json:"parameters"`
}

func main() {
	out := map[string]catalog{}
	for _, f := range families {
		c, err := capture(f)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", f.Family, err)
			os.Exit(1)
		}
		sort.Slice(c.Parameters, func(i, j int) bool { return c.Parameters[i].Name < c.Parameters[j].Name })
		out[f.Family] = c
		fmt.Printf("%s: %d parameters from %s\n", f.Family, len(c.Parameters), f.Image)
	}
	var b bytes.Buffer
	b.WriteString("{\n")
	names := make([]string, 0, len(out))
	for name := range out {
		names = append(names, name)
	}
	sort.Strings(names)
	for i, name := range names {
		c := out[name]
		head, _ := json.Marshal(struct {
			Image         string `json:"image"`
			ImageID       string `json:"imageId"`
			EngineVersion string `json:"engineVersion"`
			Source        string `json:"source"`
		}{c.Image, c.ImageID, c.EngineVersion, c.Source})
		fmt.Fprintf(&b, "%q: %s, \"parameters\": [\n", name, strings.TrimSuffix(string(head), "}"))
		params := c.Parameters
		for j, p := range params {
			line, _ := json.Marshal(p)
			b.Write(line)
			if j < len(params)-1 {
				b.WriteString(",")
			}
			b.WriteString("\n")
		}
		b.WriteString("]}")
		if i < len(names)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("}\n")
	if err := os.WriteFile("simulator-aws/rds_parameter_catalogs_vendored.json", b.Bytes(), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func docker(stdin string, args ...string) (string, error) {
	cmd := exec.Command("docker", args...)
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err != nil {
		return stdout.String() + stderr.String(), fmt.Errorf("docker %s: %w: %s", args[0], err, stderr.String())
	}
	return stdout.String(), nil
}

func capture(f family) (catalog, error) {
	imageID, err := docker("", "image", "inspect", "--format", "{{.Id}}", f.Image)
	if err != nil {
		return catalog{}, err
	}
	env := []string{"-e", "POSTGRES_HOST_AUTH_METHOD=trust", "-e", "MYSQL_ALLOW_EMPTY_PASSWORD=yes", "-e", "MARIADB_ALLOW_EMPTY_ROOT_PASSWORD=1"}
	run := append(append([]string{"run", "-d", "--rm", "--platform", "linux/amd64"}, env...), f.Image)
	id, err := docker("", append(run, f.Args...)...)
	if err != nil {
		return catalog{}, err
	}
	container := strings.TrimSpace(id)
	defer func() { _, _ = docker("", "rm", "-f", "-v", container) }()
	if err := awaitServing(f, container); err != nil {
		return catalog{}, err
	}
	c := catalog{Image: f.Image, ImageID: strings.TrimSpace(imageID)}
	switch f.Kind {
	case "postgres":
		c.Source = "pg_settings"
		err = capturePostgres(container, &c)
	case "mariadb":
		c.Source = "information_schema.SYSTEM_VARIABLES"
		err = captureMariaDB(container, &c)
	default:
		c.Source = "performance_schema.global_variables, performance_schema.variables_info, mysqld --verbose --help, SET GLOBAL"
		err = captureMySQL(container, &c)
	}
	return c, err
}

// awaitServing waits for the server the entrypoint starts after its
// initialisation, which is the first one listening on TCP.
func awaitServing(f family, container string) error {
	probe := []string{"exec", container, "pg_isready", "-h", "127.0.0.1", "-U", "postgres"}
	if f.Kind == "mysql" {
		probe = []string{"exec", container, "mysqladmin", "--protocol=tcp", "-h", "127.0.0.1", "-uroot", "ping"}
	} else if f.Kind == "mariadb" {
		probe = []string{"exec", container, "mariadb-admin", "--protocol=tcp", "-h", "127.0.0.1", "-uroot", "ping"}
	}
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		if _, err := docker("", probe...); err == nil {
			return nil
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("%s did not start serving", f.Image)
}

const postgresQuery = `SELECT json_agg(json_build_object(
  'name', name,
  'value', CASE WHEN source IN ('client', 'session') THEN boot_val ELSE reset_val END,
  'context', context, 'vartype', vartype, 'enumvals', array_to_string(enumvals, ','),
  'min', min_val, 'max', max_val, 'description', short_desc) ORDER BY name),
  current_setting('server_version')
FROM pg_settings`

func capturePostgres(container string, c *catalog) error {
	out, err := docker("", "exec", container, "psql", "-h", "127.0.0.1", "-U", "postgres", "-AtX", "-F", "\t", "-c", postgresQuery)
	if err != nil {
		return err
	}
	rows, version, _ := strings.Cut(strings.TrimSpace(out), "\t")
	c.EngineVersion = version
	var settings []struct {
		Name, Value, Context, Vartype, Enumvals, Min, Max, Description string
	}
	if err := json.Unmarshal([]byte(rows), &settings); err != nil {
		return err
	}
	for _, s := range settings {
		p := parameter{Name: s.Name, Value: s.Value, Description: s.Description, ApplyType: "dynamic", Modifiable: s.Context != "internal"}
		if s.Context == "postmaster" || s.Context == "internal" {
			p.ApplyType = "static"
		}
		switch s.Vartype {
		case "bool":
			p.DataType, p.AllowedValues = "boolean", "0,1"
			p.Value = map[string]string{"on": "1", "off": "0"}[s.Value]
		case "integer", "real":
			p.DataType, p.AllowedValues = s.Vartype, s.Min+"-"+s.Max
		case "enum":
			p.DataType, p.AllowedValues = "string", s.Enumvals
		default:
			p.DataType = "string"
		}
		c.Parameters = append(c.Parameters, p)
	}
	return nil
}

const mariadbQuery = `SET SESSION group_concat_max_len = 1073741824;
SELECT JSON_ARRAYAGG(JSON_OBJECT(
  'name', LOWER(VARIABLE_NAME), 'value', GLOBAL_VALUE, 'type', VARIABLE_TYPE, 'comment', VARIABLE_COMMENT,
  'min', NUMERIC_MIN_VALUE, 'max', NUMERIC_MAX_VALUE, 'values', ENUM_VALUE_LIST, 'readOnly', READ_ONLY,
  'option', COMMAND_LINE_ARGUMENT)), VERSION()
FROM information_schema.SYSTEM_VARIABLES WHERE VARIABLE_SCOPE <> 'SESSION ONLY';`

func captureMariaDB(container string, c *catalog) error {
	out, err := docker(mariadbQuery, "exec", "-i", container, "mariadb", "-uroot", "-N", "-B", "--raw")
	if err != nil {
		return err
	}
	rows, version, _ := strings.Cut(strings.TrimSpace(out), "\t")
	c.EngineVersion = version
	var variables []struct {
		Name, Type, Comment, ReadOnly string
		Value, Min, Max, Values       *string
		Option                        *string
	}
	if err := json.Unmarshal([]byte(rows), &variables); err != nil {
		return err
	}
	text := func(s *string) string {
		if s == nil {
			return ""
		}
		return *s
	}
	for _, v := range variables {
		p := parameter{Name: v.Name, Value: text(v.Value), Description: v.Comment, ApplyType: "dynamic"}
		if v.ReadOnly == "YES" {
			p.ApplyType = "static"
		}
		p.Modifiable = v.Option != nil
		switch {
		case v.Type == "BOOLEAN":
			p.DataType, p.AllowedValues = "boolean", "0,1"
			p.Value = map[string]string{"ON": "1", "OFF": "0"}[p.Value]
		case strings.Contains(v.Type, "INT"):
			p.DataType, p.AllowedValues = "integer", trimNumber(text(v.Min))+"-"+trimNumber(text(v.Max))
		case v.Type == "DOUBLE":
			p.DataType, p.AllowedValues = "float", trimNumber(text(v.Min))+"-"+trimNumber(text(v.Max))
		case v.Type == "SET":
			p.DataType, p.AllowedValues = "list", text(v.Values)
		case v.Type == "ENUM":
			p.DataType, p.AllowedValues = "string", text(v.Values)
		default:
			p.DataType = "string"
		}
		c.Parameters = append(c.Parameters, p)
	}
	return nil
}

func trimNumber(value string) string {
	if strings.Contains(value, ".") {
		value = strings.TrimRight(strings.TrimRight(value, "0"), ".")
	}
	return value
}

const mysqlQuery = `SELECT JSON_ARRAYAGG(JSON_OBJECT('name', g.VARIABLE_NAME, 'value', g.VARIABLE_VALUE,
  'min', i.MIN_VALUE, 'max', i.MAX_VALUE)), VERSION()
FROM performance_schema.global_variables g JOIN performance_schema.variables_info i USING (VARIABLE_NAME);`

var readOnlyVariable = regexp.MustCompile(`ERROR 1238 \(HY000\) at line \d+: Variable '([^']+)' is a read only variable`)

func captureMySQL(container string, c *catalog) error {
	out, err := docker(mysqlQuery, "exec", "-i", container, "mysql", "-uroot", "-N", "-B", "--raw")
	if err != nil {
		return err
	}
	rows, version, _ := strings.Cut(strings.TrimSpace(out), "\t")
	c.EngineVersion = version
	var variables []struct{ Name, Value, Min, Max string }
	if err := json.Unmarshal([]byte(rows), &variables); err != nil {
		return err
	}
	var probe strings.Builder
	for _, v := range variables {
		fmt.Fprintf(&probe, "SET GLOBAL %s = @@GLOBAL.%s;\n", v.Name, v.Name)
	}
	// --force runs every statement whatever the ones before it answered.
	refusals, err := exec.Command("sh", "-c", "docker exec -i "+container+" mysql -uroot --force 2>&1 <<'EOF'\n"+probe.String()+"EOF\n").Output()
	if err != nil {
		return err
	}
	static := map[string]bool{}
	for _, match := range readOnlyVariable.FindAllStringSubmatch(string(refusals), -1) {
		static[strings.ToLower(match[1])] = true
	}
	help, err := docker("", "exec", container, "mysqld", "--user=mysql", "--verbose", "--help")
	if err != nil {
		return err
	}
	options, descriptions := parseMySQLHelp(help)
	for _, v := range variables {
		p := parameter{Name: v.Name, Value: v.Value, ApplyType: "dynamic"}
		if options[v.Name] {
			p.Description = descriptions[v.Name]
		}
		if static[v.Name] {
			p.ApplyType = "static"
		}
		p.Modifiable = options[v.Name]
		numeric := v.Min != "0" || v.Max != "0"
		switch {
		case !numeric && (v.Value == "ON" || v.Value == "OFF"):
			p.DataType, p.AllowedValues = "boolean", "0,1"
			p.Value = map[string]string{"ON": "1", "OFF": "0"}[v.Value]
		case numeric && strings.Contains(v.Value, "."):
			p.DataType, p.AllowedValues = "float", trimNumber(v.Min)+"-"+trimNumber(v.Max)
		case numeric:
			if _, err := strconv.ParseFloat(v.Value, 64); err != nil && v.Value != "" {
				p.DataType = "string"
				break
			}
			p.DataType, p.AllowedValues = "integer", v.Min+"-"+v.Max
		default:
			p.DataType = "string"
		}
		c.Parameters = append(c.Parameters, p)
	}
	return nil
}

var helpOption = regexp.MustCompile(`^  (?:-[A-Za-z], )?--([a-z0-9][a-z0-9_-]*)(?:\[?=[^ ]*)?\s+(.*)$`)

// parseMySQLHelp reads the options `mysqld --verbose --help` lists, with their
// descriptions, and the variable table that follows them.
func parseMySQLHelp(help string) (map[string]bool, map[string]string) {
	options := map[string]bool{}
	descriptions := map[string]string{}
	lines := strings.Split(help, "\n")
	current := ""
	table := false
	for _, line := range lines {
		if strings.HasPrefix(line, "----------") {
			table = true
			continue
		}
		if table {
			fields := strings.Fields(line)
			if len(fields) == 0 {
				break
			}
			options[strings.ReplaceAll(fields[0], "-", "_")] = true
			continue
		}
		if match := helpOption.FindStringSubmatch(line); match != nil {
			current = strings.ReplaceAll(match[1], "-", "_")
			descriptions[current] = strings.TrimSpace(match[2])
			continue
		}
		if current != "" && strings.HasPrefix(line, "                      ") {
			descriptions[current] = strings.TrimSpace(descriptions[current] + " " + strings.TrimSpace(line))
			continue
		}
		current = ""
	}
	return options, descriptions
}
