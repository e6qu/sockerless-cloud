package main

import (
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Firestore pipeline operations, as the vendored firestore-pipeline-*.md.gz
// pages describe them and cloud.google.com/go/firestore's pipeline_stage.go
// and pipeline_function.go put them on the wire: a pipeline is an ordered list
// of stages, each a name with argument and option Values, and an expression is
// a Value that may be a field reference, a variable reference, a function or a
// nested pipeline.

type fsPipeline struct {
	Stages []fsPipelineStage `json:"stages"`
}

type fsPipelineStage struct {
	Name    string             `json:"name"`
	Args    []FSValue          `json:"args,omitempty"`
	Options map[string]FSValue `json:"options,omitempty"`
}

type fsFunction struct {
	Name    string             `json:"name"`
	Args    []FSValue          `json:"args,omitempty"`
	Options map[string]FSValue `json:"options,omitempty"`
}

// fpErr is an evaluation error: the pipeline stops and the call fails with it.
type fpErr struct{ msg string }

func (e *fpErr) Error() string { return e.msg }

func fpErrorf(format string, args ...any) error { return &fpErr{msg: fmt.Sprintf(format, args...)} }

// pval is an evaluated expression: a value, or the absent value a reference to
// a missing field evaluates to.
type pval struct {
	v      FSValue
	absent bool
}

var fpAbsent = pval{absent: true}

func fpOf(v FSValue) pval { return pval{v: v} }

func fpNull() pval { return pval{v: FSValue{NullValue: "NULL_VALUE"}} }

func fpBool(b bool) pval { return pval{v: FSValue{BooleanValue: &b}} }

func fpInt(n int64) pval { return pval{v: FSValue{IntegerValue: strconv.FormatInt(n, 10)}} }

func fpDouble(f float64) pval { return pval{v: FSValue{DoubleValue: &f}} }

func fpString(s string) pval { return pval{v: FSValue{StringValue: &s}} }

func fpTimestamp(t time.Time) pval {
	return pval{v: FSValue{TimestampValue: t.UTC().Truncate(time.Microsecond).Format(fsTimestampLayout)}}
}

func fpArray(values []FSValue) pval {
	if values == nil {
		values = []FSValue{}
	}
	return pval{v: FSValue{ArrayValue: &FSArrayValue{Values: values}}}
}

func fpMap(fields map[string]FSValue) pval {
	if fields == nil {
		fields = map[string]FSValue{}
	}
	return pval{v: FSValue{MapValue: &FSMapValue{Fields: fields}}}
}

// The value kinds in the order firestore-data-types.md.gz sorts mixed types.
const (
	fkNull = iota
	fkBool
	fkNumber
	fkTimestamp
	fkString
	fkBytes
	fkReference
	fkGeoPoint
	fkArray
	fkVector
	fkMap
	fkExpression
)

func fpKind(v FSValue) int {
	switch {
	case v.NullValue != nil:
		return fkNull
	case v.BooleanValue != nil:
		return fkBool
	case v.IntegerValue != "" || v.DoubleValue != nil:
		return fkNumber
	case v.TimestampValue != "":
		return fkTimestamp
	case v.StringValue != nil:
		return fkString
	case v.BytesValue != nil:
		return fkBytes
	case v.ReferenceValue != "":
		return fkReference
	case v.GeoPointValue != nil:
		return fkGeoPoint
	case v.ArrayValue != nil:
		return fkArray
	case v.MapValue != nil:
		if _, ok := fpVectorOf(v); ok {
			return fkVector
		}
		return fkMap
	case v.FieldReferenceValue != "" || v.VariableReferenceValue != "" || v.FunctionValue != nil || v.PipelineValue != nil:
		return fkExpression
	}
	return fkNull
}

// fpVectorOf reads a vector, which Firestore stores as a map holding
// __type__ "__vector__" and its elements under value.
func fpVectorOf(v FSValue) ([]float64, bool) {
	if v.MapValue == nil {
		return nil, false
	}
	t, ok := v.MapValue.Fields["__type__"]
	if !ok || t.StringValue == nil || *t.StringValue != "__vector__" {
		return nil, false
	}
	elems, ok := v.MapValue.Fields["value"]
	if !ok || elems.ArrayValue == nil {
		return nil, false
	}
	out := make([]float64, 0, len(elems.ArrayValue.Values))
	for _, e := range elems.ArrayValue.Values {
		f, isNum := fpFloat(e)
		if !isNum {
			return nil, false
		}
		out = append(out, f)
	}
	return out, true
}

func fpIsInt(v FSValue) bool { return v.IntegerValue != "" }

func fpIntOf(v FSValue) (int64, bool) {
	if v.IntegerValue == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(v.IntegerValue, 10, 64)
	return n, err == nil
}

func fpFloat(v FSValue) (float64, bool) {
	if v.DoubleValue != nil {
		return *v.DoubleValue, true
	}
	if n, ok := fpIntOf(v); ok {
		return float64(n), true
	}
	return 0, false
}

func fpTimeOf(v FSValue) (time.Time, bool) {
	if v.TimestampValue == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, v.TimestampValue)
	return t, err == nil
}

func fpBytesOf(v FSValue) ([]byte, bool) {
	if v.BytesValue == nil {
		return nil, false
	}
	return fsDecodeBytes(*v.BytesValue), true
}

func fpBytes(b []byte) pval {
	s := fsEncodeBytes(b)
	return pval{v: FSValue{BytesValue: &s}}
}

// fpReferencePath is a reference's path below the database's documents, the
// path its segments are compared by.
func fpReferencePath(ref string) string {
	if _, rest, ok := strings.Cut(ref, "/documents"); ok && strings.HasPrefix(ref, "projects/") {
		return strings.Trim(rest, "/")
	}
	return strings.Trim(ref, "/")
}

// fpCompare orders two values as the sort stage and cmp do, following
// firestore-data-types.md.gz: across types by the value type order, numbers
// numerically with NaN below -Infinity, strings and bytes by byte order,
// references by path segment, geo points by latitude then longitude, arrays and
// vectors element by element then by length (vectors by dimension first), maps
// by sorted key-value pairs then by size.
func fpCompare(a, b FSValue) int {
	ka, kb := fpKind(a), fpKind(b)
	if ka != kb {
		return cmpInt(ka, kb)
	}
	switch ka {
	case fkNull:
		return 0
	case fkBool:
		return cmpBool(*a.BooleanValue, *b.BooleanValue)
	case fkNumber:
		return fpCompareNumbers(a, b)
	case fkTimestamp:
		return fsTimestampOrder(a.TimestampValue, b.TimestampValue)
	case fkString:
		return strings.Compare(*a.StringValue, *b.StringValue)
	case fkBytes:
		ab, _ := fpBytesOf(a)
		bb, _ := fpBytesOf(b)
		return strings.Compare(string(ab), string(bb))
	case fkReference:
		as, bs := strings.Split(fpReferencePath(a.ReferenceValue), "/"), strings.Split(fpReferencePath(b.ReferenceValue), "/")
		for i := 0; i < len(as) && i < len(bs); i++ {
			if c := strings.Compare(as[i], bs[i]); c != 0 {
				return c
			}
		}
		return cmpInt(len(as), len(bs))
	case fkGeoPoint:
		if c := cmpFloat(a.GeoPointValue.Latitude, b.GeoPointValue.Latitude); c != 0 {
			return c
		}
		return cmpFloat(a.GeoPointValue.Longitude, b.GeoPointValue.Longitude)
	case fkArray:
		av, bv := a.ArrayValue.Values, b.ArrayValue.Values
		for i := 0; i < len(av) && i < len(bv); i++ {
			if c := fpCompare(av[i], bv[i]); c != 0 {
				return c
			}
		}
		return cmpInt(len(av), len(bv))
	case fkVector:
		av, _ := fpVectorOf(a)
		bv, _ := fpVectorOf(b)
		if c := cmpInt(len(av), len(bv)); c != 0 {
			return c
		}
		for i := range av {
			if c := fpCompareFloat(av[i], bv[i]); c != 0 {
				return c
			}
		}
		return 0
	case fkMap:
		ak, bk := fpSortedKeys(a.MapValue.Fields), fpSortedKeys(b.MapValue.Fields)
		for i := 0; i < len(ak) && i < len(bk); i++ {
			if c := strings.Compare(ak[i], bk[i]); c != 0 {
				return c
			}
			if c := fpCompare(a.MapValue.Fields[ak[i]], b.MapValue.Fields[bk[i]]); c != 0 {
				return c
			}
		}
		return cmpInt(len(ak), len(bk))
	}
	return 0
}

func fpSortedKeys(m map[string]FSValue) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func fpCompareNumbers(a, b FSValue) int {
	ai, aInt := fpIntOf(a)
	bi, bInt := fpIntOf(b)
	if aInt && bInt {
		return cmpInt64(ai, bi)
	}
	af, _ := fpFloat(a)
	bf, _ := fpFloat(b)
	return fpCompareFloat(af, bf)
}

// fpCompareFloat orders NaN below -Infinity, as Firestore normalizes it.
func fpCompareFloat(a, b float64) int {
	switch {
	case math.IsNaN(a) && math.IsNaN(b):
		return 0
	case math.IsNaN(a):
		return -1
	case math.IsNaN(b):
		return 1
	}
	return cmpFloat(a, b)
}

func cmpInt(a, b int) int { return cmpInt64(int64(a), int64(b)) }
func cmpInt64(a, b int64) int {
	return map[bool]int{true: -1, false: map[bool]int{true: 1, false: 0}[a > b]}[a < b]
}
func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
func cmpBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case !a:
		return -1
	}
	return 1
}

// fpEqual is equal(...)'s equality: numbers by value across integer and
// floating point, NaN equal to NaN, and composite values element by element.
func fpEqual(a, b FSValue) bool {
	if fpKind(a) != fpKind(b) {
		return false
	}
	if fpKind(a) == fkNumber {
		af, _ := fpFloat(a)
		bf, _ := fpFloat(b)
		if math.IsNaN(af) || math.IsNaN(bf) {
			return math.IsNaN(af) && math.IsNaN(bf)
		}
		return fpCompareNumbers(a, b) == 0
	}
	if fpKind(a) == fkArray {
		if len(a.ArrayValue.Values) != len(b.ArrayValue.Values) {
			return false
		}
		for i := range a.ArrayValue.Values {
			if !fpEqual(a.ArrayValue.Values[i], b.ArrayValue.Values[i]) {
				return false
			}
		}
		return true
	}
	if fpKind(a) == fkMap || fpKind(a) == fkVector {
		if len(a.MapValue.Fields) != len(b.MapValue.Fields) {
			return false
		}
		for k, av := range a.MapValue.Fields {
			bv, ok := b.MapValue.Fields[k]
			if !ok || !fpEqual(av, bv) {
				return false
			}
		}
		return true
	}
	return fpCompare(a, b) == 0
}

// fpComparable reports whether the ordering comparisons may order a and b:
// values of one type other than NaN, as comparison-functions.md.gz's examples
// show (a string and a number, or NaN and a number, compare FALSE either way).
func fpComparable(a, b pval) bool {
	if a.absent || b.absent || fpKind(a.v) != fpKind(b.v) {
		return false
	}
	if fpKind(a.v) == fkNumber {
		af, _ := fpFloat(a.v)
		bf, _ := fpFloat(b.v)
		return !math.IsNaN(af) && !math.IsNaN(bf)
	}
	return true
}

// fpRow is one document flowing through a pipeline: its fields, with the
// document's name and times held as the __name__, __create_time__ and
// __update_time__ fields the stages address them by.
type fpRow map[string]FSValue

const (
	fpNameField       = "__name__"
	fpCreateTimeField = "__create_time__"
	fpUpdateTimeField = "__update_time__"
)

func fpRowOf(doc FSDocument) fpRow {
	row := make(fpRow, len(doc.Fields)+3)
	for k, v := range doc.Fields {
		row[k] = v
	}
	row[fpNameField] = FSValue{ReferenceValue: doc.Name}
	if doc.CreateTime != "" {
		row[fpCreateTimeField] = FSValue{TimestampValue: doc.CreateTime}
	}
	if doc.UpdateTime != "" {
		row[fpUpdateTimeField] = FSValue{TimestampValue: doc.UpdateTime}
	}
	return row
}

// fpDocumentOf is the result document of a row: the metadata fields it still
// carries become the document's name and times, the rest its fields.
func fpDocumentOf(row fpRow) FSDocument {
	doc := FSDocument{Fields: map[string]FSValue{}}
	for k, v := range row {
		switch {
		case k == fpLetField:
		case k == fpNameField && v.ReferenceValue != "":
			doc.Name = v.ReferenceValue
		case k == fpCreateTimeField && v.TimestampValue != "":
			doc.CreateTime = v.TimestampValue
		case k == fpUpdateTimeField && v.TimestampValue != "":
			doc.UpdateTime = v.TimestampValue
		default:
			doc.Fields[k] = v
		}
	}
	return doc
}

// fpEnv is what an expression evaluates against beyond its row: the database
// the pipeline reads, the variables let(...) defined, and the request time
// current_timestamp() answers.
type fpEnv struct {
	project, database string
	vars              map[string]pval
	now               time.Time
}

func (e *fpEnv) withVars(extra map[string]pval) *fpEnv {
	vars := make(map[string]pval, len(e.vars)+len(extra))
	for k, v := range e.vars {
		vars[k] = v
	}
	for k, v := range extra {
		vars[k] = v
	}
	return &fpEnv{project: e.project, database: e.database, vars: vars, now: e.now}
}

func (e *fpEnv) documentsRoot() string {
	return fmt.Sprintf("projects/%s/databases/%s/documents", e.project, e.database)
}

// fpSplitFieldPath splits a field path on dots outside backquotes, the
// quoting Firestore field paths use for a segment holding special characters.
func fpSplitFieldPath(path string) []string {
	var segs []string
	var cur strings.Builder
	quoted := false
	for i := 0; i < len(path); i++ {
		c := path[i]
		switch {
		case c == '`':
			quoted = !quoted
		case c == '\\' && quoted && i+1 < len(path):
			i++
			cur.WriteByte(path[i])
		case c == '.' && !quoted:
			segs = append(segs, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	return append(segs, cur.String())
}

func fpGetPath(row fpRow, path string) pval {
	segs := fpSplitFieldPath(path)
	v, ok := row[segs[0]]
	if !ok {
		return fpAbsent
	}
	for _, seg := range segs[1:] {
		if v.MapValue == nil {
			return fpAbsent
		}
		v, ok = v.MapValue.Fields[seg]
		if !ok {
			return fpAbsent
		}
	}
	return fpOf(v)
}

// fpSetPath assigns a field, creating the parent maps a nested path needs, as
// add_fields(...) and select(...) do; an absent value removes the field.
func fpSetPath(row fpRow, path string, value pval) {
	segs := fpSplitFieldPath(path)
	if len(segs) == 1 {
		if value.absent {
			delete(row, segs[0])
		} else {
			row[segs[0]] = value.v
		}
		return
	}
	parent := row[segs[0]]
	child := fpRow{}
	if parent.MapValue != nil {
		for k, v := range parent.MapValue.Fields {
			child[k] = v
		}
	}
	fpSetPath(child, strings.Join(segs[1:], "."), value)
	row[segs[0]] = FSValue{MapValue: &FSMapValue{Fields: child}}
}

func fpRemovePath(row fpRow, path string) {
	segs := fpSplitFieldPath(path)
	if len(segs) == 1 {
		delete(row, segs[0])
		return
	}
	parent, ok := row[segs[0]]
	if !ok || parent.MapValue == nil {
		return
	}
	child := fpRow{}
	for k, v := range parent.MapValue.Fields {
		child[k] = v
	}
	fpRemovePath(child, strings.Join(segs[1:], "."))
	row[segs[0]] = FSValue{MapValue: &FSMapValue{Fields: child}}
}

// fpEval evaluates an expression Value against a row. A plain Value is a
// constant; arrays and maps evaluate their elements, which may be
// expressions themselves.
func fpEval(env *fpEnv, row fpRow, expr FSValue) (pval, error) {
	switch {
	case expr.FieldReferenceValue != "":
		return fpGetPath(row, expr.FieldReferenceValue), nil
	case expr.VariableReferenceValue != "":
		v, ok := env.vars[expr.VariableReferenceValue]
		if !ok {
			return pval{}, fpErrorf("Undefined variable: %s", expr.VariableReferenceValue)
		}
		return v, nil
	case expr.FunctionValue != nil:
		return fpCall(env, row, expr.FunctionValue)
	case expr.PipelineValue != nil:
		return pval{}, fpErrorf("a pipeline is not a value; wrap it in array(...) or scalar(...)")
	case expr.ArrayValue != nil:
		out := make([]FSValue, 0, len(expr.ArrayValue.Values))
		for _, e := range expr.ArrayValue.Values {
			v, err := fpEval(env, row, e)
			if err != nil {
				return pval{}, err
			}
			if v.absent {
				v = fpNull()
			}
			out = append(out, v.v)
		}
		return fpArray(out), nil
	case expr.MapValue != nil:
		out := make(map[string]FSValue, len(expr.MapValue.Fields))
		for k, e := range expr.MapValue.Fields {
			v, err := fpEval(env, row, e)
			if err != nil {
				return pval{}, err
			}
			if !v.absent {
				out[k] = v.v
			}
		}
		return fpMap(out), nil
	}
	return fpOf(expr), nil
}

// fpExecute runs a pipeline over the database and answers its result rows.
func fpExecute(env *fpEnv, p *fsPipeline, input []fpRow) ([]fpRow, error) {
	if p == nil || len(p.Stages) == 0 {
		return nil, fpErrorf("a pipeline needs at least one stage")
	}
	rows := input
	for i, stage := range p.Stages {
		var err error
		if fpInputStages[stage.Name] && i != 0 {
			return nil, fpErrorf("the %s(...) stage must be the first stage of a pipeline", stage.Name)
		}
		if i == 0 && !fpInputStages[stage.Name] && input == nil {
			return nil, fpErrorf("a pipeline must begin with an input stage, not %s(...)", stage.Name)
		}
		rows, env, err = fpRunStage(env, stage, rows)
		if err != nil {
			return nil, err
		}
	}
	return rows, nil
}

var fpInputStages = map[string]bool{
	"collection": true, "collection_group": true, "database": true, "documents": true,
	"literals": true, "subcollection": true,
}

func fpStageArg(stage fsPipelineStage, i int) (FSValue, error) {
	if i >= len(stage.Args) {
		return FSValue{}, fpErrorf("the %s(...) stage takes at least %d arguments", stage.Name, i+1)
	}
	return stage.Args[i], nil
}

func fpRunStage(env *fpEnv, stage fsPipelineStage, rows []fpRow) ([]fpRow, *fpEnv, error) {
	switch stage.Name {
	case "collection":
		ref, err := fpStageArg(stage, 0)
		if err != nil {
			return nil, env, err
		}
		path := strings.Trim(ref.ReferenceValue, "/")
		if path == "" {
			return nil, env, fpErrorf("collection(...) takes the collection's path as a reference")
		}
		prefix := env.documentsRoot() + "/" + path + "/"
		return fpDocs(fsDocumentsUnder(prefix, func(d FSDocument) bool {
			return !strings.Contains(strings.TrimPrefix(d.Name, prefix), "/")
		})), env, nil
	case "collection_group":
		ancestor, err := fpStageArg(stage, 0)
		if err != nil {
			return nil, env, err
		}
		id, err := fpStageArg(stage, 1)
		if err != nil {
			return nil, env, err
		}
		if id.StringValue == nil {
			return nil, env, fpErrorf("collection_group(...) takes the collection ID as a string")
		}
		prefix := env.documentsRoot() + "/"
		if a := strings.Trim(ancestor.ReferenceValue, "/"); a != "" {
			prefix += a + "/"
		}
		return fpDocs(fsDocumentsUnder(prefix, func(d FSDocument) bool {
			segs := strings.Split(strings.TrimPrefix(d.Name, env.documentsRoot()+"/"), "/")
			return len(segs) >= 2 && segs[len(segs)-2] == *id.StringValue
		})), env, nil
	case "database":
		return fpDocs(fsDocumentsUnder(env.documentsRoot()+"/", func(FSDocument) bool { return true })), env, nil
	case "documents":
		seen := map[string]bool{}
		var out []fpRow
		for _, ref := range stage.Args {
			path := strings.Trim(ref.ReferenceValue, "/")
			if path == "" {
				return nil, env, fpErrorf("documents(...) takes document references")
			}
			if seen[path] {
				return nil, env, fpErrorf("documents(...) names %s more than once", path)
			}
			seen[path] = true
			if doc, ok := fsDocuments.Get(env.documentsRoot() + "/" + path); ok {
				out = append(out, fpRowOf(doc))
			}
		}
		return out, env, nil
	case "literals":
		out := make([]fpRow, 0, len(stage.Args))
		for _, lit := range stage.Args {
			v, err := fpEval(env, fpRow{}, lit)
			if err != nil {
				return nil, env, err
			}
			if v.absent || v.v.MapValue == nil {
				return nil, env, fpErrorf("literals(...) takes maps, one per document")
			}
			out = append(out, fpRow(v.v.MapValue.Fields))
		}
		return out, env, nil
	case "subcollection":
		return nil, env, fpErrorf("subcollection(...) runs only as a sub-pipeline, inside array(...) or scalar(...)")
	case "where":
		cond, err := fpStageArg(stage, 0)
		if err != nil {
			return nil, env, err
		}
		var out []fpRow
		for _, row := range rows {
			v, err := fpEval(env.rowEnv(row), row, cond)
			if err != nil {
				return nil, env, err
			}
			if !v.absent && v.v.BooleanValue != nil && *v.v.BooleanValue {
				out = append(out, row)
			}
		}
		return out, env, nil
	case "select", "add_fields":
		spec, err := fpStageArg(stage, 0)
		if err != nil {
			return nil, env, err
		}
		if spec.MapValue == nil {
			return nil, env, fpErrorf("%s(...) takes a map of aliases to expressions", stage.Name)
		}
		out := make([]fpRow, 0, len(rows))
		for _, row := range rows {
			next := fpRow{}
			if stage.Name == "add_fields" {
				for k, v := range row {
					next[k] = v
				}
			} else if bound, ok := row[fpLetField]; ok {
				next[fpLetField] = bound
			}
			for _, alias := range fpSortedKeys(spec.MapValue.Fields) {
				v, err := fpEval(env.rowEnv(row), row, spec.MapValue.Fields[alias])
				if err != nil {
					return nil, env, err
				}
				fpSetPath(next, alias, v)
			}
			out = append(out, next)
		}
		return out, env, nil
	case "remove_fields":
		out := make([]fpRow, 0, len(rows))
		for _, row := range rows {
			next := fpRow{}
			for k, v := range row {
				next[k] = v
			}
			for _, f := range stage.Args {
				if f.FieldReferenceValue == "" {
					return nil, env, fpErrorf("remove_fields(...) takes field references")
				}
				fpRemovePath(next, f.FieldReferenceValue)
			}
			out = append(out, next)
		}
		return out, env, nil
	case "sort":
		return fpSort(env, stage, rows)
	case "limit", "offset":
		n, err := fpStageArg(stage, 0)
		if err != nil {
			return nil, env, err
		}
		count, ok := fpIntOf(n)
		if !ok || count < 0 {
			return nil, env, fpErrorf("%s(...) takes a non-negative integer", stage.Name)
		}
		if stage.Name == "limit" {
			if int64(len(rows)) > count {
				rows = rows[:count]
			}
			return rows, env, nil
		}
		if int64(len(rows)) <= count {
			return nil, env, nil
		}
		return rows[count:], env, nil
	case "distinct":
		spec, err := fpStageArg(stage, 0)
		if err != nil {
			return nil, env, err
		}
		if spec.MapValue == nil {
			return nil, env, fpErrorf("distinct(...) takes a map of aliases to expressions")
		}
		groups, _, err := fpGroup(env, rows, spec.MapValue.Fields)
		if err != nil {
			return nil, env, err
		}
		out := make([]fpRow, 0, len(groups))
		for _, g := range groups {
			out = append(out, g.key)
		}
		return out, fpScopeReset(env), nil
	case "aggregate":
		return fpAggregate(env, stage, rows)
	case "replace_with":
		return fpReplaceWith(env, stage, rows)
	case "union":
		other, err := fpStageArg(stage, 0)
		if err != nil {
			return nil, env, err
		}
		if other.PipelineValue == nil {
			return nil, env, fpErrorf("union(...) takes a pipeline")
		}
		more, err := fpExecute(env, other.PipelineValue, nil)
		if err != nil {
			return nil, env, err
		}
		return append(append([]fpRow{}, rows...), more...), env, nil
	case "unnest":
		return fpUnnest(env, stage, rows)
	case "sample":
		return fpSample(env, stage, rows)
	case "let":
		spec, err := fpStageArg(stage, 0)
		if err != nil {
			return nil, env, err
		}
		if spec.MapValue == nil {
			return nil, env, fpErrorf("let(...) takes a map of variable names to expressions")
		}
		return fpLet(env, rows, spec.MapValue.Fields)
	case "find_nearest":
		return fpFindNearest(env, stage, rows)
	}
	return nil, env, fpErrorf("unknown stage %q", stage.Name)
}

// fpLetField carries the variables let(...) bound for a row along with the
// row, under a key no field path can spell, until a stage merges rows.
const fpLetField = "\x00let"

// rowEnv is the environment an expression over row sees: a let(...) binds
// a variable per row, so a row carries the bindings its own stage made.
func (e *fpEnv) rowEnv(row fpRow) *fpEnv {
	bound, ok := row[fpLetField]
	if !ok || bound.MapValue == nil {
		return e
	}
	vars := make(map[string]pval, len(bound.MapValue.Fields))
	for k, v := range bound.MapValue.Fields {
		vars[k] = fpOf(v)
	}
	return e.withVars(vars)
}

func fpDocs(docs []FSDocument) []fpRow {
	out := make([]fpRow, 0, len(docs))
	for _, d := range docs {
		out = append(out, fpRowOf(d))
	}
	return out
}

func fpSort(env *fpEnv, stage fsPipelineStage, rows []fpRow) ([]fpRow, *fpEnv, error) {
	type ordering struct {
		expr FSValue
		desc bool
	}
	var orders []ordering
	for _, arg := range stage.Args {
		if arg.MapValue == nil {
			return nil, env, fpErrorf("sort(...) takes orderings")
		}
		expr, ok := arg.MapValue.Fields["expression"]
		if !ok {
			return nil, env, fpErrorf("an ordering needs an expression")
		}
		dir := arg.MapValue.Fields["direction"]
		if dir.StringValue == nil || (*dir.StringValue != "ascending" && *dir.StringValue != "descending") {
			return nil, env, fpErrorf("an ordering's direction is ascending or descending")
		}
		orders = append(orders, ordering{expr: expr, desc: *dir.StringValue == "descending"})
	}
	keys := make([][]FSValue, len(rows))
	for i, row := range rows {
		for _, o := range orders {
			v, err := fpEval(env.rowEnv(row), row, o.expr)
			if err != nil {
				return nil, env, err
			}
			// sort-stage.md.gz: a missing field sorts as if it were null.
			if v.absent {
				v = fpNull()
			}
			keys[i] = append(keys[i], v.v)
		}
	}
	idx := make([]int, len(rows))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		for j, o := range orders {
			c := fpCompare(keys[idx[a]][j], keys[idx[b]][j])
			if o.desc {
				c = -c
			}
			if c != 0 {
				return c < 0
			}
		}
		// sort-stage.md.gz: the document name breaks ties.
		return fpCompare(rows[idx[a]][fpNameField], rows[idx[b]][fpNameField]) < 0
	})
	out := make([]fpRow, len(rows))
	for i, j := range idx {
		out[i] = rows[j]
	}
	return out, env, nil
}

type fpGroupEntry struct {
	key  fpRow
	vals []FSValue
	rows []fpRow
}

// fpGroup partitions rows by the values of the grouping expressions, with
// equality semantics: equivalent values, 1 and 1.0 among them, fall in one
// group (aggregate-stage.md.gz).
func fpGroup(env *fpEnv, rows []fpRow, spec map[string]FSValue) ([]*fpGroupEntry, []string, error) {
	aliases := fpSortedKeys(spec)
	var groups []*fpGroupEntry
	for _, row := range rows {
		vals := make([]FSValue, len(aliases))
		for i, alias := range aliases {
			v, err := fpEval(env.rowEnv(row), row, spec[alias])
			if err != nil {
				return nil, nil, err
			}
			if v.absent {
				v = fpNull()
			}
			vals[i] = v.v
		}
		var into *fpGroupEntry
		for _, g := range groups {
			same := true
			for i := range vals {
				if !fpEqual(g.vals[i], vals[i]) {
					same = false
					break
				}
			}
			if same {
				into = g
				break
			}
		}
		if into == nil {
			key := fpRow{}
			for i, alias := range aliases {
				fpSetPath(key, alias, fpOf(vals[i]))
			}
			into = &fpGroupEntry{key: key, vals: vals}
			groups = append(groups, into)
		}
		into.rows = append(into.rows, row)
	}
	return groups, aliases, nil
}

// fpScopeReset ends the variables' scope: let-stage.md.gz scopes variables up
// to the first stage that merges documents together.
func fpScopeReset(env *fpEnv) *fpEnv {
	return &fpEnv{project: env.project, database: env.database, vars: map[string]pval{}, now: env.now}
}

func fpAggregate(env *fpEnv, stage fsPipelineStage, rows []fpRow) ([]fpRow, *fpEnv, error) {
	targets, err := fpStageArg(stage, 0)
	if err != nil {
		return nil, env, err
	}
	if targets.MapValue == nil || len(targets.MapValue.Fields) == 0 {
		return nil, env, fpErrorf("aggregate(...) takes at least one accumulator")
	}
	var groupSpec map[string]FSValue
	if len(stage.Args) > 1 && stage.Args[1].MapValue != nil {
		groupSpec = stage.Args[1].MapValue.Fields
	}
	var groups []*fpGroupEntry
	if len(groupSpec) == 0 {
		groups = []*fpGroupEntry{{key: fpRow{}, rows: rows}}
	} else if groups, _, err = fpGroup(env, rows, groupSpec); err != nil {
		return nil, env, err
	}
	out := make([]fpRow, 0, len(groups))
	for _, g := range groups {
		row := fpRow{}
		for k, v := range g.key {
			row[k] = v
		}
		for _, alias := range fpSortedKeys(targets.MapValue.Fields) {
			acc := targets.MapValue.Fields[alias]
			if acc.FunctionValue == nil {
				return nil, env, fpErrorf("accumulator %q is not an aggregate function", alias)
			}
			v, err := fpAccumulate(env, g.rows, acc.FunctionValue)
			if err != nil {
				return nil, env, err
			}
			fpSetPath(row, alias, v)
		}
		out = append(out, row)
	}
	return out, fpScopeReset(env), nil
}

func fpReplaceWith(env *fpEnv, stage fsPipelineStage, rows []fpRow) ([]fpRow, *fpEnv, error) {
	expr, err := fpStageArg(stage, 0)
	if err != nil {
		return nil, env, err
	}
	mode := "full_replace"
	if len(stage.Args) > 1 && stage.Args[1].StringValue != nil {
		mode = *stage.Args[1].StringValue
	}
	if mode != "full_replace" && mode != "merge_overwrite_existing" && mode != "merge_keep_existing" {
		return nil, env, fpErrorf("replace_with(...) mode %q is not one of full_replace, merge_overwrite_existing, merge_keep_existing", mode)
	}
	out := make([]fpRow, 0, len(rows))
	for _, row := range rows {
		v, err := fpEval(env.rowEnv(row), row, expr)
		if err != nil {
			return nil, env, err
		}
		if v.absent || v.v.MapValue == nil {
			return nil, env, fpErrorf("replace_with(...) needs an expression that evaluates to a map")
		}
		next := fpRow{}
		if mode != "full_replace" {
			for k, fv := range row {
				next[k] = fv
			}
		} else if bound, ok := row[fpLetField]; ok {
			next[fpLetField] = bound
		}
		for k, fv := range v.v.MapValue.Fields {
			if _, exists := next[k]; mode == "merge_keep_existing" && exists {
				continue
			}
			next[k] = fv
		}
		out = append(out, next)
	}
	return out, env, nil
}

func fpUnnest(env *fpEnv, stage fsPipelineStage, rows []fpRow) ([]fpRow, *fpEnv, error) {
	expr, err := fpStageArg(stage, 0)
	if err != nil {
		return nil, env, err
	}
	alias, err := fpStageArg(stage, 1)
	if err != nil {
		return nil, env, err
	}
	if alias.FieldReferenceValue == "" {
		return nil, env, fpErrorf("unnest(...) takes the alias as a field reference")
	}
	indexField := ""
	if idx, ok := stage.Options["index_field"]; ok {
		indexField = idx.FieldReferenceValue
		if indexField == "" && idx.StringValue != nil {
			indexField = *idx.StringValue
		}
	}
	var out []fpRow
	for _, row := range rows {
		v, err := fpEval(env.rowEnv(row), row, expr)
		if err != nil {
			return nil, env, err
		}
		if v.absent || v.v.ArrayValue == nil {
			// unnest-stage.md.gz: a non-array leaves the document as it is,
			// with the index field NULL.
			next := fpCopyRow(row)
			if indexField != "" {
				fpSetPath(next, indexField, fpNull())
			}
			out = append(out, next)
			continue
		}
		for i, elem := range v.v.ArrayValue.Values {
			next := fpCopyRow(row)
			fpSetPath(next, alias.FieldReferenceValue, fpOf(elem))
			if indexField != "" {
				fpSetPath(next, indexField, fpInt(int64(i)))
			}
			out = append(out, next)
		}
	}
	return out, env, nil
}

func fpCopyRow(row fpRow) fpRow {
	next := make(fpRow, len(row))
	for k, v := range row {
		next[k] = v
	}
	return next
}

func fpSample(env *fpEnv, stage fsPipelineStage, rows []fpRow) ([]fpRow, *fpEnv, error) {
	size, err := fpStageArg(stage, 0)
	if err != nil {
		return nil, env, err
	}
	mode, err := fpStageArg(stage, 1)
	if err != nil {
		return nil, env, err
	}
	if mode.StringValue == nil {
		return nil, env, fpErrorf("sample(...) takes a mode, documents or percent")
	}
	switch *mode.StringValue {
	case "documents":
		n, ok := fpIntOf(size)
		if !ok || n < 0 {
			return nil, env, fpErrorf("sample(...) in documents mode takes a non-negative integer")
		}
		shuffled := append([]fpRow{}, rows...)
		rand.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		if int64(len(shuffled)) > n {
			shuffled = shuffled[:n]
		}
		return shuffled, env, nil
	case "percent":
		p, ok := fpFloat(size)
		if !ok || p < 0 || p > 1 {
			return nil, env, fpErrorf("sample(...) in percent mode takes a double between 0.0 and 1.0")
		}
		var out []fpRow
		for _, row := range rows {
			if rand.Float64() < p {
				out = append(out, row)
			}
		}
		return out, env, nil
	}
	return nil, env, fpErrorf("sample(...) mode %q is not documents or percent", *mode.StringValue)
}

func fpLet(env *fpEnv, rows []fpRow, spec map[string]FSValue) ([]fpRow, *fpEnv, error) {
	out := make([]fpRow, 0, len(rows))
	for _, row := range rows {
		bound := map[string]FSValue{}
		if prior, ok := row[fpLetField]; ok && prior.MapValue != nil {
			for k, v := range prior.MapValue.Fields {
				bound[k] = v
			}
		}
		for _, name := range fpSortedKeys(spec) {
			v, err := fpEval(env.rowEnv(row), row, spec[name])
			if err != nil {
				return nil, env, err
			}
			if v.absent {
				v = fpNull()
			}
			bound[name] = v.v
		}
		next := fpCopyRow(row)
		next[fpLetField] = FSValue{MapValue: &FSMapValue{Fields: bound}}
		out = append(out, next)
	}
	return out, env, nil
}

func fpFindNearest(env *fpEnv, stage fsPipelineStage, rows []fpRow) ([]fpRow, *fpEnv, error) {
	field, err := fpStageArg(stage, 0)
	if err != nil {
		return nil, env, err
	}
	query, err := fpStageArg(stage, 1)
	if err != nil {
		return nil, env, err
	}
	measure, err := fpStageArg(stage, 2)
	if err != nil {
		return nil, env, err
	}
	qv, ok := fpVectorOf(query)
	if !ok || measure.StringValue == nil {
		return nil, env, fpErrorf("find_nearest(...) takes a field, a query vector and a distance measure")
	}
	type scored struct {
		row  fpRow
		dist float64
	}
	var hits []scored
	for _, row := range rows {
		v, err := fpEval(env.rowEnv(row), row, field)
		if err != nil {
			return nil, env, err
		}
		dv, ok := fpVectorOf(v.v)
		if v.absent || !ok || len(dv) != len(qv) {
			continue
		}
		d, err := fpVectorDistance(*measure.StringValue, dv, qv)
		if err != nil {
			return nil, env, err
		}
		hits = append(hits, scored{row: row, dist: d})
	}
	desc := *measure.StringValue == "dot_product"
	sort.SliceStable(hits, func(i, j int) bool {
		if desc {
			return hits[i].dist > hits[j].dist
		}
		return hits[i].dist < hits[j].dist
	})
	if lim, ok := stage.Options["limit"]; ok {
		if n, isInt := fpIntOf(lim); isInt && int64(len(hits)) > n {
			hits = hits[:n]
		}
	}
	out := make([]fpRow, 0, len(hits))
	distField := stage.Options["distance_field"].FieldReferenceValue
	for _, h := range hits {
		row := h.row
		if distField != "" {
			row = fpCopyRow(row)
			fpSetPath(row, distField, fpDouble(h.dist))
		}
		out = append(out, row)
	}
	return out, env, nil
}

func fpVectorDistance(measure string, a, b []float64) (float64, error) {
	switch measure {
	case "euclidean":
		var s float64
		for i := range a {
			s += (a[i] - b[i]) * (a[i] - b[i])
		}
		return math.Sqrt(s), nil
	case "cosine":
		var dot, na, nb float64
		for i := range a {
			dot += a[i] * b[i]
			na += a[i] * a[i]
			nb += b[i] * b[i]
		}
		if na == 0 || nb == 0 {
			return 0, fpErrorf("cosine distance is undefined for a zero vector")
		}
		return 1 - dot/(math.Sqrt(na)*math.Sqrt(nb)), nil
	case "dot_product":
		var dot float64
		for i := range a {
			dot += a[i] * b[i]
		}
		return dot, nil
	case "manhattan":
		var s float64
		for i := range a {
			s += math.Abs(a[i] - b[i])
		}
		return s, nil
	}
	return 0, fpErrorf("distance measure %q is not euclidean, cosine or dot_product", measure)
}

// fsRunPipeline executes a structured pipeline for executePipeline over REST
// and gRPC alike, and answers the result documents.
func fsRunPipeline(project, database string, sp *fsStructuredPipeline, now time.Time) ([]FSDocument, error) {
	if sp == nil || sp.Pipeline == nil {
		return nil, fpErrorf("executePipeline needs the pipeline to run")
	}
	env := &fpEnv{project: project, database: database, vars: map[string]pval{}, now: now}
	rows, err := fpExecute(env, sp.Pipeline, nil)
	if err != nil {
		return nil, err
	}
	docs := make([]FSDocument, 0, len(rows))
	for _, row := range rows {
		docs = append(docs, fpDocumentOf(row))
	}
	return docs, nil
}

type fsStructuredPipeline struct {
	Pipeline *fsPipeline        `json:"pipeline"`
	Options  map[string]FSValue `json:"options,omitempty"`
}

func fpIsErr(err error) bool {
	var e *fpErr
	return errors.As(err, &e)
}
