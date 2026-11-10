package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"
)

// parseToOCSF follows the mapping dossiers Amazon Security Lake publishes in
// ocsf/examples (mappings/markdown/AWS/v1.1.0 and v1.5.0) for the OCSF version
// the processor names, typing every value by that version's schema.

type cwoType struct {
	base, caption string
	observable    int
}

type cwoAttr struct {
	typ   string
	array bool
}

type cwoClass struct {
	uid             int
	caption         string
	categoryUID     int
	categoryCaption string
}

type cwoSchema struct {
	version string
	types   map[string]cwoType
	attrs   map[string]cwoAttr
	// enums holds the dictionary's enumerations by attribute name and the
	// classes' and objects' own by "<class or object>.<attribute>".
	enums       map[string]map[int]string
	scopedTypes map[string]string
	classes     map[string]cwoClass
}

type cwoDossier struct {
	version, source, dir string
	constants            [][2]string
	direct               [][2]string
	conditional          [][2]string
}

// The guide's ocsfVersion values and the dossier versions they name.
var cwoVersions = map[string]string{"V1.1": "1.1.0", "V1.5": "1.5.0"}

// The guide's "Current latest version" of mappingVersion.
const cwoLatestMappingVersion = "1.5.0"

var cwoClassOfDossier = map[string]string{
	"CloudTrail/API Activity":   "api_activity",
	"CloudTrail/Account Change": "account_change",
	"CloudTrail/Authentication": "authentication",
	"Route53":                   "dns_activity",
	"VPC Flowlogs":              "network_activity",
	"EKS Audit Logs":            "api_activity",
	"WAF":                       "http_activity",
}

func cwoDossierFor(version, dir string) *cwoDossier {
	for i := range cwoDossiers {
		if d := &cwoDossiers[i]; d.version == version && d.dir == dir {
			return d
		}
	}
	return nil
}

func cwtCompileParseToOCSF(c *cwtParseToOCSF) (cwtStep, error) {
	switch c.EventSource {
	case "CloudTrail", "Route53Resolver", "VPCFlow", "EKSAudit", "AWSWAF":
	default:
		return nil, cwtInvalid("parseToOCSF.eventSource must be one of CloudTrail, Route53Resolver, VPCFlow, EKSAudit, AWSWAF")
	}
	version, ok := cwoVersions[c.OcsfVersion]
	if !ok {
		return nil, cwtInvalid("parseToOCSF.ocsfVersion must be V1.1 or V1.5")
	}
	if c.MappingVersion != nil {
		if !cwtMappingVersionRE.MatchString(*c.MappingVersion) || len(*c.MappingVersion) > 10 {
			return nil, cwtInvalid("parseToOCSF.mappingVersion must match ^\\d+\\.\\d+(\\.\\d+)?$")
		}
		if c.OcsfVersion == "V1.1" {
			return nil, cwtInvalid("parseToOCSF.mappingVersion is not supported when ocsfVersion is V1.1")
		}
		if *c.MappingVersion != cwoLatestMappingVersion {
			return nil, cwtInvalid("parseToOCSF.mappingVersion %s is not available; the available mapping version is %s", *c.MappingVersion, cwoLatestMappingVersion)
		}
	}
	if c.Source != nil && *c.Source != "@message" {
		if err := cwtCheckKey("parseToOCSF.source", *c.Source, 128); err != nil {
			return nil, err
		}
	}
	m := &cwoMapper{schema: cwoSchemas[version], version: version, source: c.EventSource}
	src := cwtStr(c.Source)
	return cwtFunc(func(ev *cwtEvent) error {
		raw, err := ev.sourceString(src)
		if err != nil {
			return err
		}
		out, err := m.transform(raw)
		if err != nil {
			return fmt.Errorf("parseToOCSF: %v", err)
		}
		for _, k := range out.keys {
			ev.root.Put(k, out.m[k])
		}
		return nil
	}), nil
}

type cwoMapper struct {
	schema          *cwoSchema
	version, source string
}

// cwoRun is one record's conversion: the raw record, the event it builds and
// the raw fields a mapping read, which stay out of unmapped.
type cwoRun struct {
	m        *cwoMapper
	class    string
	rec      *cwtObj
	out      *cwtObj
	consumed map[string]bool
}

// The default flow log format, the format a flow log that publishes to
// CloudWatch Logs writes unless it names its own.
var cwoVPCDefaultFields = []string{
	"version", "account_id", "interface_id", "srcaddr", "dstaddr", "srcport", "dstport",
	"protocol", "packets", "bytes", "start", "end", "action", "log_status",
}

func (m *cwoMapper) record(msg string) (*cwtObj, error) {
	if m.source == "VPCFlow" {
		values := strings.Fields(msg)
		if len(values) != len(cwoVPCDefaultFields) {
			return nil, fmt.Errorf("expected the %d fields of the default flow log format, found %d", len(cwoVPCDefaultFields), len(values))
		}
		rec := newCWTObj()
		for i, v := range values {
			rec.Put(cwoVPCDefaultFields[i], v)
		}
		return rec, nil
	}
	v, err := cwtDecode(msg)
	if err != nil {
		return nil, fmt.Errorf("the message is not valid JSON: %v", err)
	}
	rec, isObj := v.(*cwtObj)
	if !isObj {
		return nil, fmt.Errorf("the message is not a JSON object")
	}
	return rec, nil
}

func (m *cwoMapper) dossierDir(rec *cwtObj) string {
	switch m.source {
	case "Route53Resolver":
		return "Route53"
	case "VPCFlow":
		return "VPC Flowlogs"
	case "EKSAudit":
		return "EKS Audit Logs"
	case "AWSWAF":
		return "WAF"
	}
	return cwoCloudTrailDossier(cwoString(rec, "eventSource"), cwoString(rec, "eventName"))
}

func (m *cwoMapper) transform(msg string) (*cwtObj, error) {
	rec, err := m.record(msg)
	if err != nil {
		return nil, err
	}
	return m.convert(rec)
}

func (m *cwoMapper) convert(rec *cwtObj) (*cwtObj, error) {
	dir := m.dossierDir(rec)
	d := cwoDossierFor(m.version, dir)
	if d == nil {
		return nil, fmt.Errorf("no OCSF %s mapping for %s", m.version, dir)
	}
	r := &cwoRun{m: m, class: cwoClassOfDossier[dir], rec: rec, out: newCWTObj(), consumed: map[string]bool{}}
	for _, c := range d.constants {
		r.setText(c[0], c[1])
	}
	r.setText("metadata.version", m.version)
	for _, row := range d.direct {
		r.mapDirect(row[0], row[1])
	}
	switch m.source {
	case "VPCFlow":
		r.vpcConditional()
	case "Route53Resolver":
		r.route53Conditional()
	case "AWSWAF":
		r.wafConditional()
	case "EKSAudit":
		r.eksConditional()
	case "CloudTrail":
		r.cloudTrailConditional(dir)
	}
	r.setActivity()
	if r.hasProfile("datetime") {
		r.addDatetimes(r.out, "")
	}
	if unmapped := r.unmapped(); len(unmapped.keys) > 0 {
		r.out.Put("unmapped", unmapped)
	}
	if obs := r.observables(); len(obs) > 0 {
		r.out.Put("observables", obs)
	}
	return cwoSortedObj(r.out), nil
}

func cwoString(o *cwtObj, path string) string {
	v, ok := cwoLookup(o, path)
	if !ok {
		return ""
	}
	s, _ := cwtScalarString(v)
	return s
}

// cwoLookup reads a dotted path, matching a key that itself holds dots, such
// as a Kubernetes annotation, before splitting it.
func cwoLookup(o *cwtObj, path string) (any, bool) {
	if v, ok := o.Get(path); ok {
		return v, true
	}
	parts := strings.Split(path, ".")
	for i := len(parts) - 1; i >= 1; i-- {
		head := strings.Join(parts[:i], ".")
		v, ok := o.Get(head)
		if !ok {
			continue
		}
		child, isObj := v.(*cwtObj)
		if !isObj {
			return nil, false
		}
		return cwoLookup(child, strings.Join(parts[i:], "."))
	}
	return nil, false
}

func (r *cwoRun) hasProfile(name string) bool {
	profiles, _ := r.out.GetPath("metadata.profiles")
	list, _ := profiles.([]any)
	for _, p := range list {
		if p == name {
			return true
		}
	}
	return false
}

// addDatetimes writes, as the datetime profile defines, a <name>_dt
// attribute beside every timestamp attribute the event carries.
func (r *cwoRun) addDatetimes(o *cwtObj, path string) {
	for _, k := range append([]string(nil), o.keys...) {
		p := cwoJoin(path, k)
		switch v := o.m[k].(type) {
		case *cwtObj:
			r.addDatetimes(v, p)
		case json.Number:
			if _, _, typ, _ := r.attrAt(p); typ != "timestamp_t" {
				continue
			}
			if _, has := o.Get(k + "_dt"); has {
				continue
			}
			if dt, ok := r.coerce(p+"_dt", v); ok {
				o.Put(k+"_dt", dt)
			}
		}
	}
}

// cwoValue is a raw value with the array indexes that reached it.
type cwoValue struct {
	v   any
	idx []int
}

// rawValues evaluates a dossier's raw expression: a dotted path whose "[]"
// segments fan out over an array, "[0]" takes the first element, "[:1]"
// every element after the first, and concat(a;b) joins two paths.
func (r *cwoRun) rawValues(expr string) []cwoValue {
	if strings.HasPrefix(expr, "concat(") && strings.HasSuffix(expr, ")") {
		parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(expr, "concat("), ")"), ";")
		var lists [][]cwoValue
		for _, p := range parts {
			lists = append(lists, r.rawValues(p))
		}
		return cwoConcat(lists)
	}
	return cwoWalk(r.rec, expr, nil, r.consumed, "")
}

func cwoConcat(lists [][]cwoValue) []cwoValue {
	byIdx := map[int][]string{}
	var order []int
	for _, l := range lists {
		for _, v := range l {
			i := -1
			if len(v.idx) > 0 {
				i = v.idx[0]
			}
			s, ok := cwtScalarString(v.v)
			if !ok || s == "" {
				continue
			}
			if _, seen := byIdx[i]; !seen {
				order = append(order, i)
			}
			byIdx[i] = append(byIdx[i], s)
		}
	}
	var out []cwoValue
	for _, i := range order {
		var idx []int
		if i >= 0 {
			idx = []int{i}
		}
		out = append(out, cwoValue{v: strings.Join(byIdx[i], "/"), idx: idx})
	}
	return out
}

func cwoWalk(cur any, path string, idx []int, consumed map[string]bool, flat string) []cwoValue {
	if path == "" {
		if consumed != nil {
			consumed[flat] = true
		}
		return []cwoValue{{v: cur, idx: idx}}
	}
	obj, isObj := cur.(*cwtObj)
	if !isObj {
		return nil
	}
	seg, rest := path, ""
	if i := strings.IndexByte(path, '.'); i >= 0 {
		seg, rest = path[:i], path[i+1:]
	}
	key, suffix := seg, ""
	if i := strings.IndexByte(seg, '['); i >= 0 {
		key, suffix = seg[:i], seg[i:]
	}
	v, ok := obj.Get(key)
	if !ok && suffix == "" && rest != "" {
		if lv, found := cwoLookup(obj, path); found {
			if consumed != nil {
				consumed[cwoJoin(flat, path)] = true
			}
			return []cwoValue{{v: lv, idx: idx}}
		}
	}
	if !ok {
		return nil
	}
	name := cwoJoin(flat, key)
	switch suffix {
	case "":
		return cwoWalk(v, rest, idx, consumed, name)
	case "[]":
		arr, isArr := v.([]any)
		if !isArr {
			return nil
		}
		var out []cwoValue
		for i, e := range arr {
			out = append(out, cwoWalk(e, rest, append(append([]int{}, idx...), i), consumed, name+"[]")...)
		}
		if consumed != nil && rest == "" {
			consumed[name+"[]"] = true
		}
		return out
	case "[0]", "[:1]":
		arr, isArr := v.([]any)
		if !isArr {
			return nil
		}
		if suffix == "[0]" {
			if len(arr) == 0 {
				return nil
			}
			return cwoWalk(arr[0], rest, idx, nil, name+"[]")
		}
		var out []cwoValue
		for i, e := range arr[min(1, len(arr)):] {
			out = append(out, cwoWalk(e, rest, append(append([]int{}, idx...), i), nil, name+"[]")...)
		}
		return out
	}
	return nil
}

func cwoJoin(a, b string) string {
	if a == "" {
		return b
	}
	return a + "." + b
}

// attrAt resolves the schema attribute at an OCSF path of the run's class: its
// scope (the class or object that holds it), its name and its definition.
func (r *cwoRun) attrAt(path string) (scope, name string, typ string, array bool) {
	scope = r.class
	segs := strings.Split(strings.ReplaceAll(path, "[]", ""), ".")
	for i, s := range segs {
		a := r.m.schema.attrs[s]
		typ = a.typ
		if t, ok := r.m.schema.scopedTypes[scope+"."+s]; ok {
			typ = t
		}
		if i == len(segs)-1 {
			return scope, s, typ, a.array
		}
		scope = typ
	}
	return
}

// base resolves a schema type to the primitive it derives from.
func (r *cwoRun) base(typ string) string {
	for i := 0; i < 8; i++ {
		t, ok := r.m.schema.types[typ]
		if !ok || t.base == "" {
			return typ
		}
		typ = t.base
	}
	return typ
}

func (r *cwoRun) enum(scope, name string) map[int]string {
	if e, ok := r.m.schema.enums[scope+"."+name]; ok {
		return e
	}
	return r.m.schema.enums[name]
}

// coerce types a raw value as the schema types the OCSF attribute at path.
func (r *cwoRun) coerce(path string, v any) (any, bool) {
	if v == nil {
		return nil, true
	}
	_, name, typ, _ := r.attrAt(path)
	if typ == "" && strings.HasSuffix(name, "_dt") {
		typ = "datetime_t"
	}
	switch r.base(typ) {
	case "integer_t", "long_t":
		if typ == "timestamp_t" {
			ms, ok := cwoEpochMillis(v)
			if !ok {
				return nil, false
			}
			return json.Number(strconv.FormatInt(ms, 10)), true
		}
		s, ok := cwtScalarString(v)
		if !ok {
			return nil, false
		}
		n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		if err != nil {
			return nil, false
		}
		return json.Number(strconv.FormatInt(n, 10)), true
	case "float_t":
		s, ok := cwtScalarString(v)
		if !ok {
			return nil, false
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
			return nil, false
		}
		return json.Number(strconv.FormatFloat(f, 'f', -1, 64)), true
	case "boolean_t":
		if b, isBool := v.(bool); isBool {
			return b, true
		}
		s, _ := cwtScalarString(v)
		switch strings.ToLower(s) {
		case "true", "yes":
			return true, true
		case "false", "no":
			return false, true
		}
		return nil, false
	case "json_t":
		if s, isStr := v.(string); isStr {
			return s, true
		}
		return cwtEncode(v), true
	}
	if typ == "datetime_t" || strings.HasSuffix(name, "_dt") {
		ms, ok := cwoEpochMillis(v)
		if !ok {
			return nil, false
		}
		return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05.000Z"), true
	}
	if s, ok := cwtScalarString(v); ok {
		return s, true
	}
	if obj, isObj := v.(*cwtObj); isObj {
		if n, ok := obj.Get("name"); ok {
			if s, isStr := n.(string); isStr {
				return s, true
			}
		}
	}
	return cwtEncode(v), true
}

// cwoEpochMillis reads a time as epoch milliseconds: a number below 1e11 is
// epoch seconds, a larger one milliseconds, and a string an RFC 3339 time.
func cwoEpochMillis(v any) (int64, bool) {
	s, ok := cwtScalarString(v)
	if !ok {
		return 0, false
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		if math.Abs(f) < 1e11 {
			return int64(math.Round(f * 1000)), true
		}
		return int64(math.Round(f)), true
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0, false
	}
	return t.UnixMilli(), true
}

// set writes values at an OCSF path. A "[]" in the path fans the values out
// over the elements of an array by the index of the raw array that held
// them; a path that ends in "[]", or names an array attribute, collects them
// all; any other path takes the first.
func (r *cwoRun) set(path string, vals []cwoValue) bool {
	present := vals[:0:0]
	for _, v := range vals {
		if v.v != nil {
			present = append(present, v)
		}
	}
	vals = present
	if len(vals) == 0 {
		return false
	}
	if i := strings.Index(path, "[]."); i >= 0 {
		head, tail := path[:i], path[i+3:]
		existing, _ := r.out.GetPath(head)
		arr, _ := existing.([]any)
		wrote := false
		for _, v := range vals {
			n := 0
			if len(v.idx) > 0 {
				n = v.idx[0]
			}
			for len(arr) <= n {
				arr = append(arr, newCWTObj())
			}
			elem, isObj := arr[n].(*cwtObj)
			if !isObj {
				continue
			}
			cv, ok := r.coerce(path, v.v)
			if !ok {
				continue
			}
			if _, has := elem.GetPath(tail); has {
				continue
			}
			elem.SetPath(tail, cv, true)
			wrote = true
		}
		if wrote {
			r.out.SetPath(head, arr, true)
		}
		return wrote
	}
	_, _, _, isArray := r.attrAt(path)
	if strings.HasSuffix(path, "[]") || isArray {
		var arr []any
		for _, v := range vals {
			for _, e := range cwoFlatten(v.v) {
				if cv, ok := r.coerce(path, e); ok {
					arr = append(arr, cv)
				}
			}
		}
		if len(arr) == 0 {
			return false
		}
		r.out.SetPath(strings.TrimSuffix(path, "[]"), arr, true)
		return true
	}
	cv, ok := r.coerce(path, vals[0].v)
	if !ok {
		return false
	}
	r.out.SetPath(path, cv, true)
	return true
}

func cwoFlatten(v any) []any {
	if arr, isArr := v.([]any); isArr {
		var out []any
		for _, e := range arr {
			out = append(out, cwoFlatten(e)...)
		}
		return out
	}
	return []any{v}
}

// setText writes a dossier constant: a bracketed list is an array of names.
func (r *cwoRun) setText(path, text string) {
	if strings.HasPrefix(text, "[") && strings.HasSuffix(text, "]") {
		var arr []any
		for _, p := range strings.Split(strings.Trim(text, "[]"), ",") {
			arr = append(arr, strings.TrimSpace(p))
		}
		r.out.SetPath(path, arr, true)
		return
	}
	if cv, ok := r.coerce(path, text); ok {
		r.out.SetPath(path, cv, true)
	}
}

// mapDirect applies a Mapping row. A constant of the dossier or an earlier
// row that wrote the attribute wins; the raw field still counts as mapped.
func (r *cwoRun) mapDirect(ocsfPath, rawExpr string) {
	vals := r.rawValues(rawExpr)
	if !strings.Contains(ocsfPath, "[].") {
		if _, has := r.out.GetPath(strings.TrimSuffix(ocsfPath, "[]")); has {
			return
		}
	}
	r.set(ocsfPath, vals)
}

func (r *cwoRun) get(rawPath string) (any, bool) {
	vals := cwoWalk(r.rec, rawPath, nil, r.consumed, "")
	if len(vals) == 0 {
		return nil, false
	}
	return vals[0].v, true
}

func (r *cwoRun) getString(rawPath string) (string, bool) {
	v, ok := r.get(rawPath)
	if !ok {
		return "", false
	}
	s, isScalar := cwtScalarString(v)
	return s, isScalar
}

func (r *cwoRun) setValue(path string, v any) {
	if cv, ok := r.coerce(path, v); ok {
		r.out.SetPath(path, cv, true)
	}
}

// setEnum writes an enumerated attribute and its caption: the enumeration
// value whose caption matches, or Other with the raw caption.
func (r *cwoRun) setEnum(idPath string, id int, caption string) {
	r.out.SetPath(idPath, json.Number(strconv.Itoa(id)), true)
	captionPath := strings.TrimSuffix(idPath, "_id")
	if idPath == "activity_id" {
		captionPath = "activity_name"
	}
	if caption != "" {
		r.out.SetPath(captionPath, caption, true)
	}
}

func (r *cwoRun) enumByCaption(idPath, caption string) (int, string, bool) {
	scope, name, _, _ := r.attrAt(idPath)
	for id, c := range r.enum(scope, name) {
		if strings.EqualFold(c, caption) {
			return id, c, true
		}
	}
	return 0, "", false
}

func (r *cwoRun) setEnumCaption(idPath, caption string) {
	if id, c, ok := r.enumByCaption(idPath, caption); ok {
		r.setEnum(idPath, id, c)
		return
	}
	r.setEnum(idPath, 99, caption)
}

func (r *cwoRun) setEnumID(idPath string, id int) {
	scope, name, _, _ := r.attrAt(idPath)
	r.setEnum(idPath, id, r.enum(scope, name)[id])
}

// setActivity writes activity_id's caption, type_uid and type_name from the
// activity the conditional mappings chose, Unknown when they chose none.
func (r *cwoRun) setActivity() {
	cls := r.m.schema.classes[r.class]
	id := 0
	if v, ok := r.out.Get("activity_id"); ok {
		num, _ := v.(json.Number)
		n, _ := strconv.Atoi(string(num))
		id = n
	} else {
		r.setEnumID("activity_id", 0)
	}
	name, _ := r.out.Get("activity_name")
	caption := r.enum(r.class, "activity_id")[id]
	r.out.Put("type_uid", json.Number(strconv.Itoa(cls.uid*100+id)))
	if caption == "" {
		caption, _ = name.(string)
	}
	r.out.Put("type_name", cls.caption+": "+caption)
}

// setAction writes action, action_id and disposition (and disposition_id
// where the dossier maps it) for a control's outcome.
func (r *cwoRun) setAction(action string, dispositionCaption string, withDispositionID bool) {
	if id, c, ok := r.enumByCaption("action_id", action); ok {
		r.setEnum("action_id", id, c)
	} else {
		r.setEnum("action_id", 99, action)
	}
	if dispositionCaption == "" {
		return
	}
	id, c, ok := r.enumByCaption("disposition_id", dispositionCaption)
	if !ok {
		id, c = 99, dispositionCaption
	}
	r.out.Put("disposition", c)
	if withDispositionID {
		r.out.Put("disposition_id", json.Number(strconv.Itoa(id)))
	}
}

func (r *cwoRun) vpcConditional() {
	action, _ := r.getString("action")
	switch action {
	case "ACCEPT":
		r.setEnumCaption("activity_id", "Traffic")
		r.setAction("Allowed", "Allowed", false)
	case "REJECT":
		r.setEnumCaption("activity_id", "Refuse")
		r.setAction("Denied", "Blocked", false)
	default:
		r.setEnumID("activity_id", 0)
		r.setAction("Unknown", "Unknown", false)
	}
	if path, ok := r.getString("traffic_path"); ok {
		n, err := strconv.Atoi(path)
		if err == nil && n >= 1 && n <= 8 {
			r.setEnumID("connection_info.boundary_id", n+3)
		} else {
			r.setEnum("connection_info.boundary_id", 99, path)
		}
	}
	switch dir, _ := r.getString("flow_direction"); dir {
	case "ingress":
		r.setEnumCaption("connection_info.direction_id", "Inbound")
	case "egress":
		r.setEnumCaption("connection_info.direction_id", "Outbound")
	default:
		r.setEnumID("connection_info.direction_id", 0)
	}
	if t, ok := r.get("type"); ok {
		r.setValue("connection_info.protocol_ver", t)
	}
	for _, side := range []struct{ addr, pkt, ep string }{{"dstaddr", "pkt_dstaddr", "dst_endpoint"}, {"srcaddr", "pkt_srcaddr", "src_endpoint"}} {
		addr, hasAddr := r.getString(side.addr)
		pkt, hasPkt := r.getString(side.pkt)
		if hasPkt && pkt != "-" && pkt != addr {
			r.setValue(side.ep+".ip", pkt)
			if hasAddr && addr != "-" {
				r.out.SetPath(side.ep+".intermediate_ips", []any{addr}, true)
			}
		} else if hasAddr && addr != "-" {
			r.setValue(side.ep+".ip", addr)
		}
	}
	dir, _ := r.getString("flow_direction")
	ep := map[string]string{"ingress": "dst_endpoint", "egress": "src_endpoint"}[dir]
	if ep == "" {
		return
	}
	for raw, attr := range map[string]string{"interface_id": "interface_uid", "vpc_id": "vpc_uid", "instance_id": "instance_uid", "subnet_id": "subnet_uid"} {
		if v, ok := r.get(raw); ok {
			r.setValue(ep+"."+attr, v)
		}
	}
}

func (r *cwoRun) route53Conditional() {
	r.setEnumCaption("activity_id", "Traffic")
	if rc, ok := r.getString("rcode"); ok {
		if code, known := cwoDNSRcodes[strings.ToUpper(rc)]; known {
			r.setEnumID("rcode_id", code)
		} else {
			r.setEnum("rcode_id", 99, rc)
		}
	}
	switch a, _ := r.getString("firewall_rule_action"); a {
	case "ALLOW":
		r.setAction("Allowed", "Allowed", false)
	case "BLOCK":
		r.setAction("Denied", "Blocked", false)
	case "ALERT":
		r.setAction("Allowed", "Alert", false)
	default:
		r.setAction("Unknown", "", false)
	}
	r.setEnumID("connection_info.direction_id", 0)
}

// The DNS RCODE mnemonics of the IANA registry (RFC 6895) that Route 53
// Resolver query logs write, by the code OCSF's rcode_id carries.
var cwoDNSRcodes = map[string]int{
	"NOERROR": 0, "FORMERR": 1, "SERVFAIL": 2, "NXDOMAIN": 3, "NOTIMP": 4, "REFUSED": 5,
	"YXDOMAIN": 6, "YXRRSET": 7, "NXRRSET": 8, "NOTAUTH": 9, "NOTZONE": 10, "DSOTYPENI": 11,
	"BADVERS": 16, "BADSIG": 16, "BADKEY": 17, "BADTIME": 18, "BADMODE": 19, "BADNAME": 20,
	"BADALG": 21, "BADTRUNC": 22, "BADCOOKIE": 23,
}

func (r *cwoRun) wafConditional() {
	if method, ok := r.getString("httpRequest.httpMethod"); ok {
		r.setEnumCaption("activity_id", method)
	}
	switch a, _ := r.getString("action"); a {
	case "ALLOW":
		r.setAction("Allowed", "Allowed", true)
	case "BLOCK":
		r.setAction("Denied", "Blocked", true)
	case "COUNT":
		r.setAction("Allowed", "Count", true)
	case "CAPTCHA":
		r.setAction("Denied", "Captcha", true)
	case "CHALLENGE":
		r.setAction("Denied", "Challenge", true)
	default:
		r.setAction("Unknown", "Unknown", true)
	}
	headers := cwoWalk(r.rec, "httpRequest.headers[]", nil, r.consumed, "")
	var all []any
	for _, h := range headers {
		obj, isObj := h.v.(*cwtObj)
		if !isObj {
			continue
		}
		name, value := cwoString(obj, "name"), cwoString(obj, "value")
		r.consumed["httpRequest.headers[].name"] = true
		r.consumed["httpRequest.headers[].value"] = true
		switch strings.ToLower(name) {
		case "user-agent":
			r.setValue("http_request.user_agent", value)
		case "host":
			r.setValue("http_request.url.hostname", value)
		case "x-forwarded-for":
			var ips []any
			for _, ip := range strings.Split(value, ",") {
				ips = append(ips, strings.TrimSpace(ip))
			}
			r.out.SetPath("http_request.x_forwarded_for", ips, true)
		case "referer":
			r.setValue("http_request.referrer", value)
		}
		hdr := newCWTObj()
		hdr.Put("name", name)
		hdr.Put("value", value)
		all = append(all, hdr)
	}
	if len(all) > 0 {
		r.out.SetPath("http_request.http_headers", all, true)
	}
	if r.m.version == "1.1.0" {
		if _, has := r.out.Get("time"); !has {
			r.mapDirect("time", "timestamp")
		}
		return
	}
	if sens := cwoWalk(r.rec, "terminatingRuleMatchDetails[].sensitivityLevel", nil, r.consumed, ""); len(sens) > 0 {
		r.setValue("firewall_rule.sensitivity", sens[0].v)
	}
	ja4, hasJA4 := r.getString("ja4Fingerprint")
	if hasJA4 {
		id, caption := r.fingerprintAlgorithm("ja4_fingerprint", "type_id", "JA4")
		fp := newCWTObj()
		fp.Put("type", caption)
		fp.Put("type_id", json.Number(strconv.Itoa(id)))
		fp.Put("value", ja4)
		r.out.Put("ja4_fingerprint_list", []any{fp})
	}
	if ja3, ok := r.getString("ja3Fingerprint"); ok && hasJA4 {
		if v := cwoJA4TLSVersion(ja4); v != "" {
			r.out.SetPath("tls.version", v, true)
		}
		id, caption := r.fingerprintAlgorithm("fingerprint", "algorithm_id", "MD5")
		r.out.SetPath("tls.ja3_hash.algorithm", caption, true)
		r.out.SetPath("tls.ja3_hash.algorithm_id", json.Number(strconv.Itoa(id)), true)
		r.out.SetPath("tls.ja3_hash.value", ja3, true)
	}
}

func (r *cwoRun) fingerprintAlgorithm(scope, attr, caption string) (int, string) {
	for id, c := range r.enum(scope, attr) {
		if strings.EqualFold(c, caption) {
			return id, c
		}
	}
	return 99, caption
}

// The JA4 fingerprint's second and third characters carry the TLS version:
// "13" is TLS 1.3, "s3" SSL 3.0.
func cwoJA4TLSVersion(ja4 string) string {
	if len(ja4) < 3 {
		return ""
	}
	switch v := ja4[1:3]; v {
	case "13", "12", "11", "10":
		return v[:1] + "." + v[1:]
	case "s3":
		return "3.0"
	case "s2":
		return "2.0"
	}
	return ""
}

func (r *cwoRun) eksConditional() {
	verb, _ := r.getString("verb")
	switch verb {
	case "create":
		r.setEnumCaption("activity_id", "Create")
	case "get", "list", "watch":
		r.setEnumCaption("activity_id", "Read")
	case "update", "patch":
		r.setEnumCaption("activity_id", "Update")
	case "delete", "deletecollection":
		r.setEnumCaption("activity_id", "Delete")
	default:
		r.setEnum("activity_id", 99, verb)
	}
	r.out.SetPath("actor.user.type_id", json.Number("0"), true)
}

// CloudTrail's three classes: Authentication for sign-in and AWS STS
// credential exchanges, Account Change for the IAM operations that change a
// principal or its policies, API Activity for every other call.
func cwoCloudTrailDossier(eventSource, eventName string) string {
	switch {
	case eventSource == "signin.amazonaws.com", eventSource == "sts.amazonaws.com" && cwoAuthenticationCalls[eventName]:
		return "CloudTrail/Authentication"
	case eventSource == "iam.amazonaws.com" && cwoAccountChangeCalls[eventName] != "":
		return "CloudTrail/Account Change"
	}
	return "CloudTrail/API Activity"
}

var cwoAuthenticationCalls = map[string]bool{
	"AssumeRole": true, "AssumeRoleWithSAML": true, "AssumeRoleWithWebIdentity": true,
	"GetFederationToken": true, "GetSessionToken": true, "AssumeRoot": true,
}

// The Account Change activity each IAM operation is.
var cwoAccountChangeCalls = map[string]string{
	"CreateUser": "Create", "CreateRole": "Create", "CreateLoginProfile": "Create", "CreateAccessKey": "Create",
	"EnableMFADevice": "MFA Factor Enable", "ChangePassword": "Password Change", "UpdateLoginProfile": "Password Reset",
	"DeactivateMFADevice": "MFA Factor Disable", "DeleteUser": "Delete", "DeleteRole": "Delete",
	"DeleteLoginProfile": "Delete", "DeleteAccessKey": "Delete",
	"AttachUserPolicy": "Attach Policy", "AttachRolePolicy": "Attach Policy", "AttachGroupPolicy": "Attach Policy",
	"PutUserPolicy": "Attach Policy", "PutRolePolicy": "Attach Policy", "PutGroupPolicy": "Attach Policy",
	"DetachUserPolicy": "Detach Policy", "DetachRolePolicy": "Detach Policy", "DetachGroupPolicy": "Detach Policy",
	"DeleteUserPolicy": "Detach Policy", "DeleteRolePolicy": "Detach Policy", "DeleteGroupPolicy": "Detach Policy",
}

// The API Activity verb an operation name starts with.
var cwoAPIVerbs = []struct{ prefix, activity string }{
	{"Create", "Create"}, {"Run", "Create"}, {"Put", "Create"}, {"Add", "Create"}, {"Register", "Create"}, {"Import", "Create"},
	{"Describe", "Read"}, {"Get", "Read"}, {"List", "Read"}, {"Head", "Read"}, {"Lookup", "Read"}, {"Search", "Read"},
	{"Select", "Read"}, {"BatchGet", "Read"}, {"Query", "Read"}, {"Scan", "Read"},
	{"Update", "Update"}, {"Modify", "Update"}, {"Set", "Update"}, {"Change", "Update"}, {"Enable", "Update"}, {"Disable", "Update"},
	{"Delete", "Delete"}, {"Remove", "Delete"}, {"Terminate", "Delete"}, {"Deregister", "Delete"},
}

func (r *cwoRun) cloudTrailConditional(dir string) {
	eventName, _ := r.getString("eventName")
	switch dir {
	case "CloudTrail/Authentication":
		if eventName == "ConsoleLogin" {
			r.setEnumCaption("activity_id", "Logon")
		} else {
			r.setEnum("activity_id", 99, eventName)
		}
	case "CloudTrail/Account Change":
		if a := cwoAccountChangeCalls[eventName]; a != "" {
			if id, c, ok := r.enumByCaption("activity_id", a); ok {
				r.setEnum("activity_id", id, c)
			} else {
				r.setEnum("activity_id", 99, eventName)
			}
		}
	default:
		activity := ""
		for _, v := range cwoAPIVerbs {
			if strings.HasPrefix(eventName, v.prefix) {
				activity = v.activity
				break
			}
		}
		if activity == "" {
			r.setEnum("activity_id", 99, eventName)
		} else {
			r.setEnumCaption("activity_id", activity)
		}
	}
	feature, ok := r.getString("eventCategory")
	if !ok {
		feature = "Management, Data, and Insights"
	}
	r.out.SetPath("metadata.product.feature.name", feature, true)
	if v, ok := r.get("requestID"); ok {
		r.setValue("api.request.uid", v)
	}
	if src, ok := r.getString("sourceIPAddress"); ok {
		if _, err := netip.ParseAddr(src); err == nil {
			r.setValue("src_endpoint.ip", src)
		} else {
			r.setValue("src_endpoint.domain", src)
		}
	}
	status := "Success"
	if _, failed := r.get("errorCode"); failed {
		status = "Failure"
	}
	if dir == "CloudTrail/Authentication" {
		status, _ = r.getString("responseElements.ConsoleLogin")
		if v, ok := r.get("userIdentity.principalId"); ok {
			r.setValue("user.uid_alt", v)
		}
		if v, ok := r.get("requestParameters.roleArn"); ok {
			r.setValue("user.uid", v)
		} else if v, ok := r.get("userIdentity.arn"); ok {
			r.setValue("user.uid", v)
		}
		if v, ok := r.get("requestParameters.roleSessionName"); ok {
			r.setValue("user.name", v)
		}
		if v, ok := r.get("additionalEventData.MFAUsed"); ok {
			r.setValue("is_mfa", v)
		}
	}
	if dir == "CloudTrail/Account Change" {
		for _, p := range []string{"responseElements.user.arn", "responseElements.role.arn"} {
			if v, ok := r.get(p); ok {
				r.setValue("user.uid", v)
				break
			}
		}
		for _, p := range []string{"responseElements.user.userId", "responseElements.role.roleId"} {
			if v, ok := r.get(p); ok {
				r.setValue("user.uid_alt", v)
				break
			}
		}
		for _, p := range []string{"requestParameters.userName", "responseElements.user.userName", "responseElements.role.roleName", "requestParameters.roleName"} {
			if v, ok := r.get(p); ok {
				r.setValue("user.name", v)
				break
			}
		}
	}
	if status != "" {
		r.out.Put("status", status)
	}
	fill := func(target, raw string) {
		if _, has := r.out.GetPath(target); has {
			return
		}
		if v, ok := r.get(raw); ok {
			r.setValue(target, v)
		}
	}
	fill("actor.user.credential_uid", "userIdentity.credentialId")
	fill("actor.user.name", "additionalEventData.UserName")
	fill("actor.user.uid_alt", "userIdentity.onBehalfOf.userId")
	fill("actor.idp.uid", "userIdentity.onBehalfOf.identityStoreArn")
}

// unmapped flattens the raw fields no mapping read: nested keys join with
// ".", an array's elements share its key with "[]", their values join with
// ",", and nulls and empty arrays and objects drop out.
func (r *cwoRun) unmapped() *cwtObj {
	values := map[string][]string{}
	var order []string
	var walk func(v any, key string)
	walk = func(v any, key string) {
		if r.consumed[key] {
			return
		}
		switch t := v.(type) {
		case *cwtObj:
			for _, k := range t.keys {
				walk(t.m[k], cwoJoin(key, k))
			}
		case []any:
			for _, e := range t {
				walk(e, key+"[]")
			}
		case nil:
		default:
			s, _ := cwtScalarString(t)
			if _, seen := values[key]; !seen {
				order = append(order, key)
			}
			values[key] = append(values[key], s)
		}
	}
	walk(r.rec, "")
	out := newCWTObj()
	for _, k := range order {
		out.Put(k, strings.Join(values[k], ","))
	}
	return out
}

// The observable kinds each dossier's Observables section names.
var cwoObservableKinds = map[string][]string{
	"VPCFlow":         {"ip", "instance"},
	"Route53Resolver": {"hostname", "ip", "instance"},
	"CloudTrail":      {"hostname", "ip", "username", "arn"},
	"EKSAudit":        {"ip", "username", "url", "resource"},
	"AWSWAF":          {"hostname", "ip", "resource"},
}

func (r *cwoRun) observables() []any {
	kinds := map[string]bool{}
	for _, k := range cwoObservableKinds[r.m.source] {
		kinds[k] = true
	}
	var out []any
	add := func(name, value, typ string) {
		t := r.m.schema.types[typ]
		o := newCWTObj()
		o.Put("name", name)
		o.Put("type", t.caption)
		o.Put("type_id", json.Number(strconv.Itoa(t.observable)))
		o.Put("value", value)
		out = append(out, o)
	}
	var walk func(v any, path string)
	walk = func(v any, path string) {
		switch t := v.(type) {
		case *cwtObj:
			keys := append([]string(nil), t.keys...)
			sort.Strings(keys)
			for _, k := range keys {
				if path == "" && k == "unmapped" {
					continue
				}
				walk(t.m[k], cwoJoin(path, k))
			}
		case []any:
			for _, e := range t {
				walk(e, path+"[]")
			}
		case string:
			if t == "" || t == "-" {
				return
			}
			_, name, typ, _ := r.attrAt(path)
			_, ipErr := netip.ParseAddr(t)
			switch {
			case kinds["ip"] && (r.base(typ) == "string_t" && typ == "ip_t" || name == "rdata" && ipErr == nil):
				add(path, t, "ip_t")
			case kinds["hostname"] && (typ == "hostname_t" || name == "domain"):
				add(path, t, "hostname_t")
			case kinds["username"] && typ == "username_t":
				add(path, t, "username_t")
			case kinds["url"] && strings.HasPrefix(path, "http_request.url.") && name == "path":
				add(path, t, "url_t")
			case kinds["instance"] && name == "instance_uid",
				kinds["arn"] && path == "resources[].uid",
				kinds["resource"] && (path == "resources[].uid" || path == "src_endpoint.uid" || path == "firewall_rule.uid"):
				add(path, t, "resource_uid_t")
			}
		}
	}
	walk(r.out, "")
	return out
}

func cwoSortedObj(t *cwtObj) *cwtObj {
	keys := append([]string(nil), t.keys...)
	sort.Strings(keys)
	o := newCWTObj()
	for _, k := range keys {
		o.Put(k, cwoSorted(t.m[k]))
	}
	return o
}

// cwoSorted orders every object's keys, as the dossiers' samples print them.
func cwoSorted(v any) any {
	switch t := v.(type) {
	case *cwtObj:
		return cwoSortedObj(t)
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = cwoSorted(e)
		}
		return out
	}
	return v
}
