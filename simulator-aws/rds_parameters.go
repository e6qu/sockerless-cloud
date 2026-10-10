package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/dbengine"
)

// rdsParameterCatalogsVendored holds each family's parameters as its engine
// reports them; scripts/capture-rds-parameter-catalogs.go writes it.
//
//go:embed rds_parameter_catalogs_vendored.json
var rdsParameterCatalogsVendored []byte

type rdsCatalogParameter struct {
	Name          string `json:"name"`
	Value         string `json:"value"`
	ApplyType     string `json:"applyType"`
	DataType      string `json:"dataType"`
	AllowedValues string `json:"allowedValues"`
	IsModifiable  bool   `json:"isModifiable"`
	Description   string `json:"description"`
}

type rdsParameterCatalog struct {
	Image      string                `json:"image"`
	Parameters []rdsCatalogParameter `json:"parameters"`
	byName     map[string]int
}

func (c *rdsParameterCatalog) lookup(name string) (rdsCatalogParameter, bool) {
	index, ok := c.byName[name]
	if !ok {
		return rdsCatalogParameter{}, false
	}
	return c.Parameters[index], true
}

// rdsManagedParameters are the settings the simulator runs every engine with
// to serve its endpoint, its automated backups, its log files and its read
// replicas, which a DB parameter group cannot change.
var rdsManagedParameters = map[dbengine.Family][]string{
	dbengine.Postgres: {
		"archive_command", "archive_library", "archive_mode", "config_file", "data_directory", "external_pid_file",
		"hba_file", "ident_file", "listen_addresses", "log_destination", "log_directory", "log_filename",
		"logging_collector", "port", "primary_conninfo", "primary_slot_name", "restore_command", "ssl",
		"ssl_ca_file", "ssl_cert_file", "ssl_crl_dir", "ssl_crl_file", "ssl_dh_params_file", "ssl_key_file",
		"ssl_passphrase_command", "ssl_passphrase_command_supports_reload", "unix_socket_directories",
		"unix_socket_group", "unix_socket_permissions", "wal_level",
	},
	dbengine.MySQL: {
		"admin_address", "admin_port", "basedir", "bind_address", "binlog_expire_logs_seconds", "datadir",
		"default_authentication_plugin", "expire_logs_days", "ignore_db_dirs", "log_bin", "log_bin_basename",
		"log_bin_index", "log_error", "mysqlx_bind_address", "mysqlx_port", "mysqlx_socket", "pid_file",
		"plugin_dir", "port", "require_secure_transport", "secure_file_priv", "server_id", "skip_networking",
		"socket", "ssl_ca", "ssl_capath", "ssl_cert", "ssl_cipher", "ssl_crl", "ssl_crlpath", "ssl_key",
	},
}

var rdsParameterCatalogs = rdsLoadParameterCatalogs()

func rdsLoadParameterCatalogs() map[string]*rdsParameterCatalog {
	var catalogs map[string]*rdsParameterCatalog
	if err := json.Unmarshal(rdsParameterCatalogsVendored, &catalogs); err != nil {
		panic(fmt.Sprintf("rds_parameter_catalogs_vendored.json: %v", err))
	}
	for family, catalog := range catalogs {
		managed := rdsManagedParameters[dbengine.MySQL]
		if strings.HasPrefix(family, "postgres") {
			managed = rdsManagedParameters[dbengine.Postgres]
		}
		catalog.byName = map[string]int{}
		for i := range catalog.Parameters {
			if slices.Contains(managed, catalog.Parameters[i].Name) {
				catalog.Parameters[i].IsModifiable = false
			}
			catalog.byName[catalog.Parameters[i].Name] = i
		}
	}
	return catalogs
}

// rdsFamilyParameterCatalog is the catalog of the engine a DB parameter group
// family runs: an Aurora family runs the community release its rows name.
func rdsFamilyParameterCatalog(family string) (*rdsParameterCatalog, bool) {
	if catalog, ok := rdsParameterCatalogs[family]; ok {
		return catalog, true
	}
	for _, row := range rdsEngineVersions {
		if row.Family != family {
			continue
		}
		for _, catalog := range rdsParameterCatalogs {
			if catalog.Image == row.Image {
				return catalog, true
			}
		}
	}
	return nil, false
}

func renderRDSCatalogParameter(p rdsCatalogParameter, set map[string]string, methods map[string]string) string {
	value, source, method := p.Value, "engine-default", "pending-reboot"
	if v, ok := set[p.Name]; ok {
		value, source = v, "user"
		if m := methods[p.Name]; m != "" {
			method = m
		}
	}
	var b strings.Builder
	b.WriteString("<Parameter>")
	fmt.Fprintf(&b, "<ParameterName>%s</ParameterName>", xmlEscape(p.Name))
	if value != "" {
		fmt.Fprintf(&b, "<ParameterValue>%s</ParameterValue>", xmlEscape(value))
	}
	if p.Description != "" {
		fmt.Fprintf(&b, "<Description>%s</Description>", xmlEscape(p.Description))
	}
	fmt.Fprintf(&b, "<Source>%s</Source>", source)
	fmt.Fprintf(&b, "<ApplyType>%s</ApplyType>", xmlEscape(p.ApplyType))
	fmt.Fprintf(&b, "<DataType>%s</DataType>", xmlEscape(p.DataType))
	if p.AllowedValues != "" {
		fmt.Fprintf(&b, "<AllowedValues>%s</AllowedValues>", xmlEscape(p.AllowedValues))
	}
	fmt.Fprintf(&b, "<IsModifiable>%t</IsModifiable>", p.IsModifiable)
	fmt.Fprintf(&b, "<ApplyMethod>%s</ApplyMethod>", method)
	b.WriteString("</Parameter>")
	return b.String()
}

// rdsParameterPage renders one page of a family's parameters, with the Marker
// that names the next page's first parameter. source filters on the
// parameters' Source when it is set.
func rdsParameterPage(r *http.Request, family string, set, methods map[string]string) (body string, code, message string) {
	maxRecords := 100
	if value := r.FormValue("MaxRecords"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 20 || n > 100 {
			return "", "InvalidParameterValue", "MaxRecords must be between 20 and 100."
		}
		maxRecords = n
	}
	source := r.FormValue("Source")
	switch source {
	case "", "user", "system", "engine-default":
	default:
		return "", "InvalidParameterValue", fmt.Sprintf("Unrecognized source: %s", source)
	}
	marker := r.FormValue("Marker")
	var b strings.Builder
	b.WriteString("<Parameters>")
	catalog, ok := rdsFamilyParameterCatalog(family)
	next := ""
	if ok {
		count := 0
		for _, p := range catalog.Parameters {
			if marker != "" && p.Name < marker {
				continue
			}
			_, userSet := set[p.Name]
			if source == "user" && !userSet || source == "engine-default" && userSet || source == "system" {
				continue
			}
			if count == maxRecords {
				next = p.Name
				break
			}
			b.WriteString(renderRDSCatalogParameter(p, set, methods))
			count++
		}
	}
	b.WriteString("</Parameters>")
	if next != "" {
		fmt.Fprintf(&b, "<Marker>%s</Marker>", xmlEscape(next))
	}
	return b.String(), "", ""
}

// rdsParameterChange is one parameter of a ModifyDBParameterGroup or
// ResetDBParameterGroup request.
type rdsParameterChange struct {
	Name, Value, ApplyMethod string
}

func parseRDSParameterChanges(r *http.Request) []rdsParameterChange {
	var changes []rdsParameterChange
	for n := 1; ; n++ {
		prefix := fmt.Sprintf("Parameters.Parameter.%d.", n)
		name := r.FormValue(prefix + "ParameterName")
		if name == "" {
			return changes
		}
		changes = append(changes, rdsParameterChange{
			Name: name, Value: r.FormValue(prefix + "ParameterValue"), ApplyMethod: r.FormValue(prefix + "ApplyMethod"),
		})
	}
}

// rdsValidateParameterChanges checks a request's changes against the
// family's catalog the way Amazon RDS does, and fills in each change's apply
// method: pending-reboot for a static parameter that names none, immediate for
// a dynamic one.
func rdsValidateParameterChanges(family string, changes []rdsParameterChange, resetting bool) (code, message string) {
	if len(changes) == 0 && !resetting {
		return "MissingParameter", "The request must contain the parameter Parameters."
	}
	if len(changes) > 20 {
		return "InvalidParameterValue", "You can modify a maximum of 20 parameters in a single request."
	}
	catalog, ok := rdsFamilyParameterCatalog(family)
	if !ok {
		return "", ""
	}
	for i, change := range changes {
		p, found := catalog.lookup(change.Name)
		if !found {
			return "InvalidParameterValue", fmt.Sprintf("Could not find parameter with name: %s", change.Name)
		}
		if !p.IsModifiable {
			return "InvalidParameterValue", fmt.Sprintf("The parameter %s cannot be modified.", change.Name)
		}
		switch change.ApplyMethod {
		case "":
			changes[i].ApplyMethod = "immediate"
			if p.ApplyType == "static" {
				changes[i].ApplyMethod = "pending-reboot"
			}
		case "immediate":
			if p.ApplyType == "static" {
				return "InvalidParameterCombination", "cannot use immediate apply method for static parameter"
			}
		case "pending-reboot":
		default:
			return "InvalidParameterValue", fmt.Sprintf("Invalid apply method: %s", change.ApplyMethod)
		}
		if resetting {
			continue
		}
		if !rdsParameterValueAllowed(p, change.Value) {
			return "InvalidParameterValue", fmt.Sprintf("Value: %s is outside of range: %s for parameter: %s", change.Value, p.AllowedValues, change.Name)
		}
	}
	return "", ""
}

// rdsParameterValueAllowed reports whether value lies within the parameter's
// AllowedValues: a range for a number, one of the listed values otherwise,
// and every listed value of a list.
func rdsParameterValueAllowed(p rdsCatalogParameter, value string) bool {
	if p.AllowedValues == "" {
		return p.DataType == "string" || p.DataType == "list"
	}
	switch p.DataType {
	case "integer", "real", "float":
		number, err := strconv.ParseFloat(value, 64)
		if err != nil || p.DataType == "integer" && strings.ContainsAny(value, ".eE") {
			return false
		}
		low, high, ok := rdsSplitRange(p.AllowedValues)
		return ok && number >= low && number <= high
	case "list":
		if value == "" {
			return true
		}
		for _, item := range strings.Split(value, ",") {
			if !rdsAllowedValue(p.AllowedValues, item) {
				return false
			}
		}
		return true
	}
	return rdsAllowedValue(p.AllowedValues, value)
}

func rdsAllowedValue(allowed, value string) bool {
	for _, candidate := range strings.Split(allowed, ",") {
		if strings.EqualFold(strings.TrimSpace(candidate), strings.TrimSpace(value)) {
			return true
		}
	}
	return false
}

// rdsSplitRange reads "low-high", where low may itself be negative.
func rdsSplitRange(allowed string) (float64, float64, bool) {
	separator := strings.Index(allowed[1:], "-")
	if separator < 0 {
		return 0, 0, false
	}
	low, lowErr := strconv.ParseFloat(allowed[:separator+1], 64)
	high, highErr := strconv.ParseFloat(allowed[separator+2:], 64)
	return low, high, lowErr == nil && highErr == nil
}

func handleRDSDescribeParameters(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("DBParameterGroupName")
	rdsEnsureOfferedDefaultParameterGroup(name)
	g, ok := rdsParamGroups.Get(name)
	if !ok {
		g, ok = findRDSParamGroupByARN(name)
		if !ok {
			rdsErrorXML(w, "DBParameterGroupNotFound",
				fmt.Sprintf("DBParameterGroup %q not found", name),
				http.StatusNotFound, sim.RequestID(r.Context()))
			return
		}
	}
	body, code, message := rdsParameterPage(r, g.DBParameterGroupFamily, g.Parameters, g.ApplyMethods)
	if code != "" {
		rdsErrorXML(w, code, message, http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	rdsXMLResponse(w, "DescribeDBParameters", body, sim.RequestID(r.Context()))
}

func handleRDSDescribeClusterParameters(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("DBClusterParameterGroupName")
	g, ok := rdsClusterParamGroups.Get(name)
	if !ok {
		g, ok = findRDSClusterParamGroupByARN(name)
		if !ok {
			rdsErrorXML(w, "DBParameterGroupNotFound",
				fmt.Sprintf("DBClusterParameterGroup %q not found", name),
				http.StatusNotFound, sim.RequestID(r.Context()))
			return
		}
	}
	body, code, message := rdsParameterPage(r, g.DBParameterGroupFamily, g.Parameters, g.ApplyMethods)
	if code != "" {
		rdsErrorXML(w, code, message, http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	rdsXMLResponse(w, "DescribeDBClusterParameters", body, sim.RequestID(r.Context()))
}

func rdsEngineDefaultsResponse(w http.ResponseWriter, r *http.Request, op string) {
	family := r.FormValue("DBParameterGroupFamily")
	if family == "" {
		rdsErrorXML(w, "MissingParameter", "The request must contain the parameter DBParameterGroupFamily.", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	page, code, message := rdsParameterPage(r, family, nil, nil)
	if code != "" {
		rdsErrorXML(w, code, message, http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	body := "<EngineDefaults>" + fmt.Sprintf("<DBParameterGroupFamily>%s</DBParameterGroupFamily>", xmlEscape(family)) + page + "</EngineDefaults>"
	rdsXMLResponse(w, op, body, sim.RequestID(r.Context()))
}

func handleRDSDescribeEngineDefaultParameters(w http.ResponseWriter, r *http.Request) {
	rdsEngineDefaultsResponse(w, r, "DescribeEngineDefaultParameters")
}

func handleRDSDescribeEngineDefaultClusterParameters(w http.ResponseWriter, r *http.Request) {
	rdsEngineDefaultsResponse(w, r, "DescribeEngineDefaultClusterParameters")
}

func handleRDSModifyParameterGroup(w http.ResponseWriter, r *http.Request) {
	rdsChangeParameterGroup(w, r, "ModifyDBParameterGroup", false)
}

func handleRDSResetParameterGroup(w http.ResponseWriter, r *http.Request) {
	rdsChangeParameterGroup(w, r, "ResetDBParameterGroup", true)
}

// rdsChangeParameterGroup sets or resets a DB parameter group's parameters,
// and applies each dynamic one whose apply method is immediate to the running
// engine of every DB instance associated with the group.
func rdsChangeParameterGroup(w http.ResponseWriter, r *http.Request, op string, resetting bool) {
	requestID := sim.RequestID(r.Context())
	name := r.FormValue("DBParameterGroupName")
	if strings.HasPrefix(name, "default.") {
		rdsErrorXML(w, "InvalidParameterValue", "Default parameter groups cannot be modified.", http.StatusBadRequest, requestID)
		return
	}
	group, ok := rdsParamGroups.Get(name)
	if !ok {
		rdsErrorXML(w, "DBParameterGroupNotFound", "DB parameter group not found", http.StatusNotFound, requestID)
		return
	}
	changes := parseRDSParameterChanges(r)
	resetAll := resetting && strings.EqualFold(r.FormValue("ResetAllParameters"), "true")
	if resetAll && len(changes) > 0 {
		rdsErrorXML(w, "InvalidParameterCombination", "ResetAllParameters cannot be specified with a list of parameters.", http.StatusBadRequest, requestID)
		return
	}
	if resetting && !resetAll && len(changes) == 0 {
		rdsErrorXML(w, "InvalidParameterCombination", "Specify either ResetAllParameters or a list of parameters.", http.StatusBadRequest, requestID)
		return
	}
	if resetAll {
		for parameter := range group.Parameters {
			changes = append(changes, rdsParameterChange{Name: parameter})
		}
		slices.SortFunc(changes, func(a, b rdsParameterChange) int { return strings.Compare(a.Name, b.Name) })
	}
	if !resetAll {
		if code, message := rdsValidateParameterChanges(group.DBParameterGroupFamily, changes, resetting); code != "" {
			rdsErrorXML(w, code, message, http.StatusBadRequest, requestID)
			return
		}
	}
	rdsParamGroups.Update(name, func(g *RDSParamGroup) {
		if g.Parameters == nil {
			g.Parameters = map[string]string{}
		}
		if g.ApplyMethods == nil {
			g.ApplyMethods = map[string]string{}
		}
		for _, change := range changes {
			if resetting {
				delete(g.Parameters, change.Name)
				delete(g.ApplyMethods, change.Name)
				continue
			}
			g.Parameters[change.Name] = change.Value
			g.ApplyMethods[change.Name] = change.ApplyMethod
		}
		group = *g
	})
	if catalog, ok := rdsFamilyParameterCatalog(group.DBParameterGroupFamily); ok {
		var immediate []string
		for _, change := range changes {
			p, _ := catalog.lookup(change.Name)
			if p.ApplyType == "dynamic" && (resetAll || change.ApplyMethod != "pending-reboot") {
				immediate = append(immediate, change.Name)
			}
		}
		rdsApplyDynamicParameters(group, catalog, immediate)
	}
	rdsXMLResponse(w, op, fmt.Sprintf("<DBParameterGroupName>%s</DBParameterGroupName>", xmlEscape(name)), requestID)
}

func handleRDSModifyClusterParameterGroup(w http.ResponseWriter, r *http.Request) {
	requestID := sim.RequestID(r.Context())
	name := r.FormValue("DBClusterParameterGroupName")
	group, ok := rdsClusterParamGroups.Get(name)
	if !ok {
		rdsErrorXML(w, "DBParameterGroupNotFound", "DB cluster parameter group not found", http.StatusNotFound, requestID)
		return
	}
	changes := parseRDSParameterChanges(r)
	if code, message := rdsValidateParameterChanges(group.DBParameterGroupFamily, changes, false); code != "" {
		rdsErrorXML(w, code, message, http.StatusBadRequest, requestID)
		return
	}
	rdsClusterParamGroups.Update(name, func(g *RDSClusterParamGroup) {
		if g.Parameters == nil {
			g.Parameters = map[string]string{}
		}
		if g.ApplyMethods == nil {
			g.ApplyMethods = map[string]string{}
		}
		for _, change := range changes {
			g.Parameters[change.Name] = change.Value
			g.ApplyMethods[change.Name] = change.ApplyMethod
		}
	})
	rdsXMLResponse(w, "ModifyDBClusterParameterGroup",
		fmt.Sprintf("<DBClusterParameterGroupName>%s</DBClusterParameterGroupName>", xmlEscape(name)), requestID)
}

// RDSEngineParameters are the parameters a DB instance's engine runs with:
// those of the DB parameter group it last started under, and the dynamic
// changes applied to it since.
type RDSEngineParameters struct {
	Group  string
	Values map[string]string
}

// rdsResolveEngineParameters is what an engine starting under the instance's
// DB parameter group runs with.
func rdsResolveEngineParameters(instance RDSInstance) *RDSEngineParameters {
	group, _ := rdsParamGroups.Get(instance.DBParameterGroupName)
	return &RDSEngineParameters{Group: instance.DBParameterGroupName, Values: maps.Clone(group.Parameters)}
}

// rdsParameterApplyStatus is the DB instance's ParameterApplyStatus: in-sync
// while its engine runs what its DB parameter group sets, and pending-reboot
// once the group or its static parameters changed since the engine started.
func rdsParameterApplyStatus(instance RDSInstance) string {
	applied := instance.EngineParameters
	if applied == nil {
		return "in-sync"
	}
	group, _ := rdsParamGroups.Get(instance.DBParameterGroupName)
	if applied.Group != instance.DBParameterGroupName || !maps.Equal(applied.Values, group.Parameters) {
		return "pending-reboot"
	}
	return "in-sync"
}

// rdsEngineParameterArgs are the command-line settings an engine starts
// with: every parameter its group sets on a MySQL-family engine, which takes
// later dynamic changes with SET GLOBAL; and the static ones on PostgreSQL,
// whose dynamic ones live in a configuration file a reload rereads, since a
// reload never overrides a command-line setting.
func rdsEngineParameterArgs(engine dbengine.Engine, instance RDSInstance) []string {
	if instance.EngineParameters == nil {
		return nil
	}
	catalog, ok := rdsFamilyParameterCatalog(rdsParameterGroupFamily(instance.Engine, instance.EngineVersion))
	if !ok {
		return nil
	}
	var args []string
	for _, name := range slices.Sorted(maps.Keys(instance.EngineParameters.Values)) {
		value := instance.EngineParameters.Values[name]
		if engine.Family == dbengine.MySQL {
			args = append(args, "--"+name+"="+value)
			continue
		}
		if p, found := catalog.lookup(name); found && p.ApplyType == "static" {
			args = append(args, "-c", name+"="+value)
		}
	}
	return args
}

// rdsPostgresParameterFile is the file in the data directory that holds a
// PostgreSQL engine's dynamic parameters, which postgresql.conf includes.
const rdsPostgresParameterFile = "rds_parameters.conf"

// rdsPostgresParameterScript writes the dynamic parameters, includes their
// file in postgresql.conf, and has the server reread its configuration. The
// postmaster rereads it asynchronously, and a session started before it has
// done so would still see the old values, so the script returns once a new
// session sees a configuration load later than the request.
const rdsPostgresParameterScript = `set -e
cd "$1"
printf '%s' "$2" > ` + rdsPostgresParameterFile + `.new
chmod 644 ` + rdsPostgresParameterFile + `.new
mv ` + rdsPostgresParameterFile + `.new ` + rdsPostgresParameterFile + `
grep -qx "include_if_exists = '` + rdsPostgresParameterFile + `'" postgresql.conf || echo "include_if_exists = '` + rdsPostgresParameterFile + `'" >> postgresql.conf
requested=$(psql -U "$3" -d "$4" -tAXc "SELECT now()")
psql -U "$3" -d "$4" -tAXc "SELECT pg_reload_conf()" > /dev/null
until [ "$(psql -U "$3" -d "$4" -tAXc "SELECT pg_conf_load_time() >= '$requested'")" = t ]; do sleep 0.05; done
`

// rdsWritePostgresParameters gives a running PostgreSQL engine its dynamic
// parameters.
func rdsWritePostgresParameters(engine *dbengine.Instance, instance RDSInstance, catalog *rdsParameterCatalog) error {
	var content strings.Builder
	if applied := instance.EngineParameters; applied != nil {
		for _, name := range slices.Sorted(maps.Keys(applied.Values)) {
			if p, found := catalog.lookup(name); found && p.ApplyType == "dynamic" {
				fmt.Fprintf(&content, "%s = %s\n", name, dbengine.QuoteLiteral(applied.Values[name]))
			}
		}
	}
	if err := engine.Exec([]string{"sh", "-c", rdsPostgresParameterScript, "sh", engine.Engine.DataPath, content.String(),
		instance.MasterUsername, rdsDatabaseName(instance)}); err != nil {
		return fmt.Errorf("apply the DB parameter group's dynamic parameters: %w", err)
	}
	return nil
}

// rdsSetMySQLParameters sets dynamic parameters on a running MySQL-family
// engine, each to the value its group sets or, once reset, its default.
func rdsSetMySQLParameters(plane *rdsDataPlane, catalog *rdsParameterCatalog, values map[string]string, names []string) error {
	password, err := plane.backendPassword()
	if err != nil {
		return err
	}
	var statements []string
	for _, name := range names {
		p, found := catalog.lookup(name)
		if !found {
			continue
		}
		value, set := values[name]
		if !set {
			value = p.Value
		}
		literal := dbengine.QuoteMySQLLiteral(value)
		switch p.DataType {
		case "integer", "float", "boolean":
			literal = value
		}
		statements = append(statements, "SET GLOBAL "+name+" = "+literal)
	}
	if len(statements) == 0 {
		return nil
	}
	return plane.engine.Exec([]string{plane.engine.Engine.Client, "--user=root", "--password=" + password,
		"--execute=" + strings.Join(statements, "; ")})
}

// applyEngineParameters gives a starting PostgreSQL engine the dynamic
// parameters its group sets; a MySQL-family engine took them all on its
// command line.
func (plane *rdsDataPlane) applyEngineParameters() error {
	if plane.engine.Engine.Family != dbengine.Postgres {
		return nil
	}
	instance, ok := rdsInstances.Get(plane.current().DBInstanceIdentifier)
	if !ok {
		instance = plane.current()
	}
	catalog, found := rdsFamilyParameterCatalog(rdsParameterGroupFamily(instance.Engine, instance.EngineVersion))
	if !found {
		return nil
	}
	return rdsWritePostgresParameters(plane.engine, instance, catalog)
}

// rdsApplyDynamicParameters applies a group's changed dynamic parameters to
// each DB instance associated with it, even one whose engine still runs the
// group it was associated with before: to the running engine at once, and to
// the parameters a stopped engine starts with.
func rdsApplyDynamicParameters(group RDSParamGroup, catalog *rdsParameterCatalog, names []string) {
	if len(names) == 0 {
		return
	}
	for _, instance := range rdsInstances.List() {
		applied := instance.EngineParameters
		if instance.DBParameterGroupName != group.DBParameterGroupName || applied == nil {
			continue
		}
		values := maps.Clone(applied.Values)
		if values == nil {
			values = map[string]string{}
		}
		for _, name := range names {
			if value, set := group.Parameters[name]; set {
				values[name] = value
			} else {
				delete(values, name)
			}
		}
		id := instance.DBInstanceIdentifier
		rdsInstances.Update(id, func(stored *RDSInstance) {
			if stored.DbiResourceId == instance.DbiResourceId && stored.EngineParameters != nil {
				stored.EngineParameters.Values = values
			}
		})
		plane, served := rdsLoadDataPlane(id)
		if !served || !plane.engine.Running() {
			continue
		}
		instance.EngineParameters = &RDSEngineParameters{Group: applied.Group, Values: values}
		var err error
		if plane.engine.Engine.Family == dbengine.Postgres {
			err = rdsWritePostgresParameters(plane.engine, instance, catalog)
		} else {
			err = rdsSetMySQLParameters(plane, catalog, values, names)
		}
		if err != nil {
			log.Printf("Amazon RDS %s: apply DB parameter group %s: %v", id, group.DBParameterGroupName, err)
		}
	}
}
