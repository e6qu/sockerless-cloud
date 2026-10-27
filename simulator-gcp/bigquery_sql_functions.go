package main

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"database/sql/driver"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	_ "time/tzdata"
	"unicode"
	"unicode/utf8"

	farm "github.com/dgryski/go-farm"
	"modernc.org/sqlite"
)

// The GoogleSQL functions SQLite lacks, registered on the driver under a
// `bq_` prefix so none shadows a SQLite built-in the simulator's own stores
// use; the rewrite renames each call. Inside the engine a TIMESTAMP is epoch
// microseconds (INTEGER), a DATE, DATETIME or TIME its canonical text, a BOOL
// 0 or 1, BYTES the stored base64 text, and an ARRAY or STRUCT its JSON.

// bqRuntimeErrPrefix marks an error a registered function raised, so the
// engine reports it as the function's error and not as a syntax error.
const bqRuntimeErrPrefix = "bq-runtime: "

type bqScalarFunc func(args []driver.Value) (driver.Value, error)

// bqFunction describes one registered GoogleSQL function: its result type,
// used to type a result column that calls it ("" leaves the type to the
// values, "ARG0" takes the first argument's type), and its implementation.
type bqFunction struct {
	returns string
	nArgs   int32
	scalar  bqScalarFunc
	// aggregate constructs the aggregate state for one group.
	aggregate func() sqlite.AggregateFunction
	// safe adds a SAFE. variant that answers NULL where the function errors.
	safe bool
}

var bqFunctions = map[string]bqFunction{
	"TIMESTAMP":           {returns: "TIMESTAMP", nArgs: -1, scalar: bqFnTimestamp, safe: true},
	"DATE":                {returns: "DATE", nArgs: -1, scalar: bqFnDate, safe: true},
	"DATETIME":            {returns: "DATETIME", nArgs: -1, scalar: bqFnDatetime, safe: true},
	"TIME":                {returns: "TIME", nArgs: -1, scalar: bqFnTime, safe: true},
	"STRING":              {returns: "STRING", nArgs: -1, scalar: bqFnString, safe: true},
	"TIMESTAMP_ADD":       {returns: "TIMESTAMP", nArgs: 3, scalar: bqFnAdd(1), safe: true},
	"TIMESTAMP_SUB":       {returns: "TIMESTAMP", nArgs: 3, scalar: bqFnAdd(-1), safe: true},
	"DATE_ADD":            {returns: "DATE", nArgs: 3, scalar: bqFnAdd(1), safe: true},
	"DATE_SUB":            {returns: "DATE", nArgs: 3, scalar: bqFnAdd(-1), safe: true},
	"DATETIME_ADD":        {returns: "DATETIME", nArgs: 3, scalar: bqFnAdd(1), safe: true},
	"DATETIME_SUB":        {returns: "DATETIME", nArgs: 3, scalar: bqFnAdd(-1), safe: true},
	"TIME_ADD":            {returns: "TIME", nArgs: 3, scalar: bqFnAdd(1), safe: true},
	"TIME_SUB":            {returns: "TIME", nArgs: 3, scalar: bqFnAdd(-1), safe: true},
	"TIMESTAMP_DIFF":      {returns: "INT64", nArgs: 3, scalar: bqFnDiff, safe: true},
	"DATE_DIFF":           {returns: "INT64", nArgs: 3, scalar: bqFnDiff, safe: true},
	"DATETIME_DIFF":       {returns: "INT64", nArgs: 3, scalar: bqFnDiff, safe: true},
	"TIME_DIFF":           {returns: "INT64", nArgs: 3, scalar: bqFnDiff, safe: true},
	"TIMESTAMP_TRUNC":     {returns: "ARG0:TIMESTAMP", nArgs: -1, scalar: bqFnTrunc, safe: true},
	"DATE_TRUNC":          {returns: "ARG0:DATE", nArgs: -1, scalar: bqFnTrunc, safe: true},
	"DATETIME_TRUNC":      {returns: "ARG0:DATETIME", nArgs: -1, scalar: bqFnTrunc, safe: true},
	"TIME_TRUNC":          {returns: "TIME", nArgs: 2, scalar: bqFnTrunc, safe: true},
	"EXTRACT":             {returns: "", nArgs: -1, scalar: bqFnExtract},
	"LAST_DAY":            {returns: "ARG0:DATE", nArgs: -1, scalar: bqFnLastDay, safe: true},
	"FORMAT_TIMESTAMP":    {returns: "STRING", nArgs: -1, scalar: bqFnFormat, safe: true},
	"FORMAT_DATE":         {returns: "STRING", nArgs: 2, scalar: bqFnFormat, safe: true},
	"FORMAT_DATETIME":     {returns: "STRING", nArgs: 2, scalar: bqFnFormat, safe: true},
	"FORMAT_TIME":         {returns: "STRING", nArgs: 2, scalar: bqFnFormat, safe: true},
	"PARSE_TIMESTAMP":     {returns: "TIMESTAMP", nArgs: -1, scalar: bqFnParse("TIMESTAMP"), safe: true},
	"PARSE_DATE":          {returns: "DATE", nArgs: 2, scalar: bqFnParse("DATE"), safe: true},
	"PARSE_DATETIME":      {returns: "DATETIME", nArgs: 2, scalar: bqFnParse("DATETIME"), safe: true},
	"PARSE_TIME":          {returns: "TIME", nArgs: 2, scalar: bqFnParse("TIME"), safe: true},
	"TIMESTAMP_MICROS":    {returns: "TIMESTAMP", nArgs: 1, scalar: bqFnTimestampUnits(1), safe: true},
	"TIMESTAMP_MILLIS":    {returns: "TIMESTAMP", nArgs: 1, scalar: bqFnTimestampUnits(1000), safe: true},
	"TIMESTAMP_SECONDS":   {returns: "TIMESTAMP", nArgs: 1, scalar: bqFnTimestampUnits(1000000), safe: true},
	"UNIX_MICROS":         {returns: "INT64", nArgs: 1, scalar: bqFnUnixUnits(1), safe: true},
	"UNIX_MILLIS":         {returns: "INT64", nArgs: 1, scalar: bqFnUnixUnits(1000), safe: true},
	"UNIX_SECONDS":        {returns: "INT64", nArgs: 1, scalar: bqFnUnixUnits(1000000), safe: true},
	"UNIX_DATE":           {returns: "INT64", nArgs: 1, scalar: bqFnUnixDate, safe: true},
	"DATE_FROM_UNIX_DATE": {returns: "DATE", nArgs: 1, scalar: bqFnDateFromUnixDate, safe: true},
	"CAST":                {returns: "", nArgs: 3, scalar: bqFnCast},
	"SAFE_CAST":           {returns: "", nArgs: 3, scalar: bqSafe(bqFnCast)},
	"SAFE_DIVIDE":         {returns: "FLOAT64", nArgs: 2, scalar: bqFnSafeDivide},
	"IEEE_DIVIDE":         {returns: "FLOAT64", nArgs: 2, scalar: bqFnIEEEDivide},
	"DIV":                 {returns: "INT64", nArgs: 2, scalar: bqFnDiv, safe: true},
	"MOD":                 {returns: "INT64", nArgs: 2, scalar: bqFnMod, safe: true},
	"SAFE_ADD":            {returns: "ARG0:INT64", nArgs: 2, scalar: bqFnSafeArith('+')},
	"SAFE_SUBTRACT":       {returns: "ARG0:INT64", nArgs: 2, scalar: bqFnSafeArith('-')},
	"SAFE_MULTIPLY":       {returns: "ARG0:INT64", nArgs: 2, scalar: bqFnSafeArith('*')},
	"SAFE_NEGATE":         {returns: "ARG0:INT64", nArgs: 1, scalar: bqFnSafeNegate},
	"TRUNC":               {returns: "FLOAT64", nArgs: -1, scalar: bqFnTrunc2, safe: true},
	"CONCAT":              {returns: "STRING", nArgs: -1, scalar: bqFnConcat},
	"ARRAY_LENGTH":        {returns: "INT64", nArgs: 1, scalar: bqFnArrayLength},
	"ARRAY_TO_STRING":     {returns: "STRING", nArgs: -1, scalar: bqFnArrayToString, safe: true},
	"ARRAY_CONCAT":        {returns: "ARG0", nArgs: -1, scalar: bqFnArrayConcat},
	"ARRAY_REVERSE":       {returns: "ARG0", nArgs: 1, scalar: bqFnArrayReverse},
	"GENERATE_ARRAY":      {returns: "ARRAY<INT64>", nArgs: -1, scalar: bqFnGenerateArray, safe: true},
	"ARRAY_AT":            {returns: "", nArgs: 3, scalar: bqFnArrayAt},
	"STARTS_WITH":         {returns: "BOOL", nArgs: 2, scalar: bqFnStartsWith},
	"ENDS_WITH":           {returns: "BOOL", nArgs: 2, scalar: bqFnEndsWith},
	"REGEXP_CONTAINS":     {returns: "BOOL", nArgs: 2, scalar: bqFnRegexpContains, safe: true},
	"REGEXP_EXTRACT":      {returns: "STRING", nArgs: -1, scalar: bqFnRegexpExtract, safe: true},
	"REGEXP_EXTRACT_ALL":  {returns: "ARRAY<STRING>", nArgs: 2, scalar: bqFnRegexpExtractAll, safe: true},
	"REGEXP_REPLACE":      {returns: "STRING", nArgs: 3, scalar: bqFnRegexpReplace, safe: true},
	"SPLIT":               {returns: "ARRAY<STRING>", nArgs: -1, scalar: bqFnSplit},
	"CHAR_LENGTH":         {returns: "INT64", nArgs: 1, scalar: bqFnCharLength},
	"CHARACTER_LENGTH":    {returns: "INT64", nArgs: 1, scalar: bqFnCharLength},
	"BYTE_LENGTH":         {returns: "INT64", nArgs: 1, scalar: bqFnByteLength},
	"STRPOS":              {returns: "INT64", nArgs: 2, scalar: bqFnStrpos},
	"REPEAT":              {returns: "STRING", nArgs: 2, scalar: bqFnRepeat, safe: true},
	"REVERSE":             {returns: "STRING", nArgs: 1, scalar: bqFnReverse},
	"LPAD":                {returns: "STRING", nArgs: -1, scalar: bqFnPad(true), safe: true},
	"RPAD":                {returns: "STRING", nArgs: -1, scalar: bqFnPad(false), safe: true},
	"INITCAP":             {returns: "STRING", nArgs: -1, scalar: bqFnInitcap},
	"TO_HEX":              {returns: "STRING", nArgs: 1, scalar: bqFnToHex, safe: true},
	"FROM_HEX":            {returns: "BYTES", nArgs: 1, scalar: bqFnFromHex, safe: true},
	"TO_BASE64":           {returns: "STRING", nArgs: 1, scalar: bqFnToBase64},
	"FROM_BASE64":         {returns: "BYTES", nArgs: 1, scalar: bqFnFromBase64, safe: true},
	"TO_JSON_STRING":      {returns: "STRING", nArgs: -1, scalar: bqFnToJSONString},
	"JSON_VALUE":          {returns: "STRING", nArgs: -1, scalar: bqFnJSONValue, safe: true},
	"JSON_EXTRACT_SCALAR": {returns: "STRING", nArgs: -1, scalar: bqFnJSONValue, safe: true},
	"JSON_QUERY":          {returns: "JSON", nArgs: -1, scalar: bqFnJSONQuery, safe: true},
	"JSON_EXTRACT":        {returns: "JSON", nArgs: -1, scalar: bqFnJSONQuery, safe: true},
	"FARM_FINGERPRINT":    {returns: "INT64", nArgs: 1, scalar: bqFnFarmFingerprint},
	"MD5":                 {returns: "BYTES", nArgs: 1, scalar: bqFnHash(func(b []byte) []byte { s := md5.Sum(b); return s[:] })},
	"SHA1":                {returns: "BYTES", nArgs: 1, scalar: bqFnHash(func(b []byte) []byte { s := sha1.Sum(b); return s[:] })},
	"SHA256":              {returns: "BYTES", nArgs: 1, scalar: bqFnHash(func(b []byte) []byte { s := sha256.Sum256(b); return s[:] })},
	"SHA512":              {returns: "BYTES", nArgs: 1, scalar: bqFnHash(func(b []byte) []byte { s := sha512.Sum512(b); return s[:] })},
	"COUNTIF":             {returns: "INT64", nArgs: 1, aggregate: func() sqlite.AggregateFunction { return &bqCountIf{} }},
	"LOGICAL_AND":         {returns: "BOOL", nArgs: 1, aggregate: func() sqlite.AggregateFunction { return &bqLogical{and: true} }},
	"LOGICAL_OR":          {returns: "BOOL", nArgs: 1, aggregate: func() sqlite.AggregateFunction { return &bqLogical{} }},
	"ANY_VALUE":           {returns: "ARG0", nArgs: 1, aggregate: func() sqlite.AggregateFunction { return &bqAnyValue{} }},
	"ARRAY_AGG":           {returns: "ARRAY:ARG0", nArgs: 1, aggregate: func() sqlite.AggregateFunction { return &bqArrayAgg{} }},
	"ARRAY_AGG_IGNORE_NULLS": {returns: "ARRAY:ARG0", nArgs: 1, aggregate: func() sqlite.AggregateFunction {
		return &bqArrayAgg{ignoreNulls: true}
	}},
	"STRING_AGG": {returns: "STRING", nArgs: -1, aggregate: func() sqlite.AggregateFunction { return &bqStringAgg{} }},
}

var bqRegisterFunctions sync.Once

func bqSQLiteName(name string) string { return "bq_" + strings.ToLower(name) }

func bqEnsureFunctions() {
	bqRegisterFunctions.Do(func() {
		for name, f := range bqFunctions {
			impl := &sqlite.FunctionImpl{NArgs: f.nArgs, Deterministic: true}
			if f.aggregate != nil {
				agg := f.aggregate
				impl.MakeAggregate = func(sqlite.FunctionContext) (sqlite.AggregateFunction, error) { return agg(), nil }
			} else {
				impl.Scalar = bqScalar(f.scalar)
			}
			sqlite.MustRegisterFunction(bqSQLiteName(name), impl)
			if f.safe {
				sqlite.MustRegisterFunction(bqSQLiteName("SAFE_"+name), &sqlite.FunctionImpl{NArgs: f.nArgs, Deterministic: true, Scalar: bqScalar(bqSafe(f.scalar))})
			}
		}
	})
}

func bqScalar(fn bqScalarFunc) func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) {
	return func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		v, err := fn(args)
		if err != nil {
			return nil, fmt.Errorf("%s%s", bqRuntimeErrPrefix, err.Error())
		}
		return v, nil
	}
}

// bqSafe is fn answering NULL instead of an error, as the SAFE. prefix asks.
func bqSafe(fn bqScalarFunc) bqScalarFunc {
	return func(args []driver.Value) (driver.Value, error) {
		v, err := fn(args)
		if err != nil {
			return nil, nil
		}
		return v, nil
	}
}

// bqTemporal is a DATE, DATETIME, TIME or TIMESTAMP value a function read
// from its SQLite form: a DATE or DATETIME as a civil time in UTC, a TIME as a
// clock on 1970-01-01, a TIMESTAMP as the instant.
type bqTemporal struct {
	kind string
	t    time.Time
}

var (
	bqDateTextRE     = regexp.MustCompile(`^\d{4}-\d{1,2}-\d{1,2}$`)
	bqTimeTextRE     = regexp.MustCompile(`^\d{1,2}:\d{2}:\d{2}(\.\d{1,9})?$`)
	bqDatetimeTextRE = regexp.MustCompile(`^\d{4}-\d{1,2}-\d{1,2}[ Tt]\d{1,2}:\d{2}:\d{2}(\.\d{1,9})?$`)
)

// bqReadTemporal classifies v: an INTEGER is a TIMESTAMP, text is the type
// whose canonical format it matches, a string with a zone a TIMESTAMP literal.
func bqReadTemporal(v driver.Value) (bqTemporal, error) {
	switch x := v.(type) {
	case int64:
		return bqTemporal{"TIMESTAMP", time.UnixMicro(x).UTC()}, nil
	case float64:
		return bqTemporal{"TIMESTAMP", time.UnixMicro(int64(x)).UTC()}, nil
	case string:
		s := strings.TrimSpace(x)
		switch {
		case bqDateTextRE.MatchString(s):
			t, err := time.Parse("2006-1-2", s)
			if err != nil {
				return bqTemporal{}, bqQueryErr("Invalid date: '%s'", x)
			}
			return bqTemporal{"DATE", t}, nil
		case bqTimeTextRE.MatchString(s):
			t, err := time.Parse("15:04:05.999999999", s)
			if err != nil {
				return bqTemporal{}, bqQueryErr("Invalid time: '%s'", x)
			}
			return bqTemporal{"TIME", t}, nil
		case bqDatetimeTextRE.MatchString(s):
			t, err := bqParseDateTime(strings.NewReplacer("T", " ", "t", " ").Replace(s))
			if err != nil {
				return bqTemporal{}, bqQueryErr("Invalid datetime: '%s'", x)
			}
			return bqTemporal{"DATETIME", t}, nil
		}
		t, err := bqParseTimestamp(x, s)
		if err != nil {
			return bqTemporal{}, bqQueryErr("Invalid timestamp: '%s'", x)
		}
		return bqTemporal{"TIMESTAMP", t}, nil
	}
	return bqTemporal{}, bqQueryErr("No matching signature for a date or time function with argument of type %s", bqSQLiteTypeName(v))
}

func bqSQLiteTypeName(v driver.Value) string {
	switch v.(type) {
	case int64:
		return "INT64"
	case float64:
		return "FLOAT64"
	case string:
		return "STRING"
	case []byte:
		return "BYTES"
	case nil:
		return "NULL"
	}
	return fmt.Sprintf("%T", v)
}

// value is the SQLite form of the temporal.
func (tv bqTemporal) value() driver.Value {
	switch tv.kind {
	case "DATE":
		return tv.t.Format("2006-01-02")
	case "TIME":
		return bqFormatClock(tv.t)
	case "DATETIME":
		return tv.t.Format("2006-01-02T") + bqFormatClock(tv.t)
	}
	return tv.t.UnixMicro()
}

// civil is the temporal's calendar reading in loc: a TIMESTAMP converts, the
// civil kinds are already civil.
func (tv bqTemporal) civil(loc *time.Location) time.Time {
	if tv.kind == "TIMESTAMP" {
		c := tv.t.In(loc)
		return time.Date(c.Year(), c.Month(), c.Day(), c.Hour(), c.Minute(), c.Second(), c.Nanosecond(), time.UTC)
	}
	return tv.t
}

// fromCivil rebuilds a temporal of tv's kind from a civil reading in loc.
func (tv bqTemporal) fromCivil(c time.Time, loc *time.Location) bqTemporal {
	if tv.kind == "TIMESTAMP" {
		return bqTemporal{"TIMESTAMP", time.Date(c.Year(), c.Month(), c.Day(), c.Hour(), c.Minute(), c.Second(), c.Nanosecond(), loc).UTC()}
	}
	return bqTemporal{tv.kind, c}
}

func bqArgString(args []driver.Value, i int) (string, bool, error) {
	if i >= len(args) || args[i] == nil {
		return "", false, nil
	}
	switch s := args[i].(type) {
	case string:
		return s, true, nil
	case []byte:
		return string(s), true, nil
	}
	return "", false, bqQueryErr("No matching signature for argument %d: expected STRING, got %s", i+1, bqSQLiteTypeName(args[i]))
}

func bqArgInt(args []driver.Value, i int) (int64, bool, error) {
	if i >= len(args) || args[i] == nil {
		return 0, false, nil
	}
	switch n := args[i].(type) {
	case int64:
		return n, true, nil
	case float64:
		if n == math.Trunc(n) {
			return int64(n), true, nil
		}
	case string:
		if v, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64); err == nil {
			return v, true, nil
		}
	}
	return 0, false, bqQueryErr("No matching signature for argument %d: expected INT64, got %s", i+1, bqSQLiteTypeName(args[i]))
}

func bqArgZone(args []driver.Value, i int) (*time.Location, error) {
	name, ok, err := bqArgString(args, i)
	if err != nil {
		return nil, err
	}
	if !ok {
		return time.UTC, nil
	}
	return bqLoadZone(name)
}

func bqAnyNil(args []driver.Value) bool {
	for _, a := range args {
		if a == nil {
			return true
		}
	}
	return false
}

// bqParseDatePart reads a part name such as DAY or WEEK(MONDAY); the weekday
// is Sunday unless the part names one.
func bqParseDatePart(part string) (name string, weekStart time.Weekday, err error) {
	p := strings.ToUpper(strings.ReplaceAll(part, " ", ""))
	if strings.HasPrefix(p, "WEEK(") && strings.HasSuffix(p, ")") {
		day := strings.TrimSuffix(strings.TrimPrefix(p, "WEEK("), ")")
		for i, n := range bqWeekdayNames {
			if strings.ToUpper(n) == day {
				return "WEEK", time.Weekday(i), nil
			}
		}
		return "", 0, bqQueryErr("A valid date part is required but found %s", part)
	}
	switch p {
	case "MICROSECOND", "MILLISECOND", "SECOND", "MINUTE", "HOUR", "DAY", "WEEK", "ISOWEEK", "MONTH", "QUARTER", "YEAR", "ISOYEAR",
		"DAYOFWEEK", "DAYOFYEAR", "DATE", "DATETIME", "TIME":
		return p, time.Sunday, nil
	}
	return "", 0, bqQueryErr("A valid date part is required but found %s", part)
}

func bqFnTimestamp(args []driver.Value) (driver.Value, error) {
	if len(args) == 0 || len(args) > 2 {
		return nil, bqQueryErr("No matching signature for function TIMESTAMP for argument count %d", len(args))
	}
	if args[0] == nil {
		return nil, nil
	}
	loc, err := bqArgZone(args, 1)
	if err != nil {
		return nil, err
	}
	tv, err := bqReadTemporal(args[0])
	if err != nil {
		return nil, err
	}
	switch tv.kind {
	case "TIMESTAMP":
		if s, ok := args[0].(string); ok && len(args) == 2 && bqZoneSuffixRE.MatchString(strings.TrimSpace(s)) {
			return nil, bqQueryErr("Invalid timestamp: '%s': a timestamp literal with a time zone takes no time zone argument", s)
		}
		if s, ok := args[0].(string); ok && len(args) == 2 {
			t, err := bqParseDateTime(strings.NewReplacer("T", " ", "t", " ").Replace(strings.TrimSpace(s)))
			if err != nil {
				return nil, bqQueryErr("Invalid timestamp: '%s'", s)
			}
			return bqTemporal{"TIMESTAMP", tv.t}.fromCivil(t, loc).value(), nil
		}
		return tv.value(), nil
	case "DATE", "DATETIME":
		return bqTemporal{kind: "TIMESTAMP"}.fromCivil(tv.t, loc).value(), nil
	}
	return nil, bqQueryErr("No matching signature for function TIMESTAMP for argument types: TIME")
}

func bqFnDate(args []driver.Value) (driver.Value, error) {
	switch len(args) {
	case 3:
		if bqAnyNil(args) {
			return nil, nil
		}
		y, _, err := bqArgInt(args, 0)
		if err != nil {
			return nil, err
		}
		m, _, err := bqArgInt(args, 1)
		if err != nil {
			return nil, err
		}
		d, _, err := bqArgInt(args, 2)
		if err != nil {
			return nil, err
		}
		t := time.Date(int(y), time.Month(m), int(d), 0, 0, 0, 0, time.UTC)
		if t.Year() != int(y) || int(t.Month()) != int(m) || t.Day() != int(d) || y < 1 || y > 9999 {
			return nil, bqQueryErr("Input calculates to invalid date: %d-%02d-%02d", y, m, d)
		}
		return bqTemporal{"DATE", t}.value(), nil
	case 1, 2:
		if args[0] == nil {
			return nil, nil
		}
		loc, err := bqArgZone(args, 1)
		if err != nil {
			return nil, err
		}
		tv, err := bqReadTemporal(args[0])
		if err != nil {
			return nil, err
		}
		if tv.kind == "TIME" {
			return nil, bqQueryErr("No matching signature for function DATE for argument types: TIME")
		}
		c := tv.civil(loc)
		return bqTemporal{"DATE", time.Date(c.Year(), c.Month(), c.Day(), 0, 0, 0, 0, time.UTC)}.value(), nil
	}
	return nil, bqQueryErr("No matching signature for function DATE for argument count %d", len(args))
}

func bqFnDatetime(args []driver.Value) (driver.Value, error) {
	switch len(args) {
	case 6:
		if bqAnyNil(args) {
			return nil, nil
		}
		parts := make([]int, 6)
		for i := range parts {
			n, _, err := bqArgInt(args, i)
			if err != nil {
				return nil, err
			}
			parts[i] = int(n)
		}
		t := time.Date(parts[0], time.Month(parts[1]), parts[2], parts[3], parts[4], parts[5], 0, time.UTC)
		if t.Day() != parts[2] || int(t.Month()) != parts[1] {
			return nil, bqQueryErr("Input calculates to invalid datetime")
		}
		return bqTemporal{"DATETIME", t}.value(), nil
	case 1, 2:
		if args[0] == nil {
			return nil, nil
		}
		tv, err := bqReadTemporal(args[0])
		if err != nil {
			return nil, err
		}
		switch tv.kind {
		case "TIMESTAMP":
			loc, err := bqArgZone(args, 1)
			if err != nil {
				return nil, err
			}
			return bqTemporal{"DATETIME", tv.civil(loc)}.value(), nil
		case "DATE":
			if len(args) == 2 && args[1] != nil {
				clock, err := bqReadTemporal(args[1])
				if err != nil {
					return nil, err
				}
				if clock.kind != "TIME" {
					return nil, bqQueryErr("No matching signature for function DATETIME for argument types: DATE, %s", clock.kind)
				}
				c := clock.t
				return bqTemporal{"DATETIME", time.Date(tv.t.Year(), tv.t.Month(), tv.t.Day(), c.Hour(), c.Minute(), c.Second(), c.Nanosecond(), time.UTC)}.value(), nil
			}
			return bqTemporal{"DATETIME", tv.t}.value(), nil
		case "DATETIME":
			return tv.value(), nil
		}
	}
	return nil, bqQueryErr("No matching signature for function DATETIME for argument count %d", len(args))
}

func bqFnTime(args []driver.Value) (driver.Value, error) {
	switch len(args) {
	case 3:
		if bqAnyNil(args) {
			return nil, nil
		}
		parts := make([]int, 3)
		for i := range parts {
			n, _, err := bqArgInt(args, i)
			if err != nil {
				return nil, err
			}
			parts[i] = int(n)
		}
		if parts[0] > 23 || parts[1] > 59 || parts[2] > 59 || parts[0] < 0 || parts[1] < 0 || parts[2] < 0 {
			return nil, bqQueryErr("Input calculates to invalid time: %02d:%02d:%02d", parts[0], parts[1], parts[2])
		}
		return bqTemporal{"TIME", time.Date(1970, 1, 1, parts[0], parts[1], parts[2], 0, time.UTC)}.value(), nil
	case 1, 2:
		if args[0] == nil {
			return nil, nil
		}
		loc, err := bqArgZone(args, 1)
		if err != nil {
			return nil, err
		}
		tv, err := bqReadTemporal(args[0])
		if err != nil {
			return nil, err
		}
		if tv.kind == "DATE" {
			return nil, bqQueryErr("No matching signature for function TIME for argument types: DATE")
		}
		c := tv.civil(loc)
		return bqTemporal{"TIME", time.Date(1970, 1, 1, c.Hour(), c.Minute(), c.Second(), c.Nanosecond(), time.UTC)}.value(), nil
	}
	return nil, bqQueryErr("No matching signature for function TIME for argument count %d", len(args))
}

// bqTimestampText is CAST(timestamp AS STRING) in loc: the canonical format
// with as many of the subsecond digits as are not trailing zeros, in groups
// of three, and the zone's offset.
func bqTimestampText(t time.Time, loc *time.Location) string {
	c := t.In(loc)
	text := c.Format("2006-01-02 15:04:05")
	switch micros := c.Nanosecond() / 1000; {
	case micros == 0:
	case micros%1000 == 0:
		text += fmt.Sprintf(".%03d", micros/1000)
	default:
		text += fmt.Sprintf(".%06d", micros)
	}
	_, offset := c.Zone()
	sign := "+"
	if offset < 0 {
		sign, offset = "-", -offset
	}
	text += fmt.Sprintf("%s%02d", sign, offset/3600)
	if offset%3600 != 0 {
		text += fmt.Sprintf(":%02d", offset%3600/60)
	}
	return text
}

func bqFnString(args []driver.Value) (driver.Value, error) {
	if len(args) == 0 || len(args) > 2 {
		return nil, bqQueryErr("No matching signature for function STRING for argument count %d", len(args))
	}
	if args[0] == nil {
		return nil, nil
	}
	loc, err := bqArgZone(args, 1)
	if err != nil {
		return nil, err
	}
	tv, err := bqReadTemporal(args[0])
	if err != nil {
		return nil, err
	}
	if tv.kind != "TIMESTAMP" {
		return nil, bqQueryErr("No matching signature for function STRING for argument types: %s", tv.kind)
	}
	return bqTimestampText(tv.t, loc), nil
}

// bqUnitMicros is the length of a fixed-length part in microseconds.
func bqUnitMicros(part string) (int64, bool) {
	switch part {
	case "MICROSECOND":
		return 1, true
	case "MILLISECOND":
		return 1000, true
	case "SECOND":
		return 1000000, true
	case "MINUTE":
		return 60 * 1000000, true
	case "HOUR":
		return 3600 * 1000000, true
	case "DAY":
		return 24 * 3600 * 1000000, true
	}
	return 0, false
}

// bqAddPart adds n units of part to a temporal, clamping a month's day to the
// resulting month's length as DATE_ADD and DATETIME_ADD do.
func bqAddPart(tv bqTemporal, n int64, part string) (bqTemporal, error) {
	fixed, isFixed := bqUnitMicros(part)
	switch tv.kind {
	case "TIMESTAMP":
		if !isFixed {
			return tv, bqQueryErr("TIMESTAMP_ADD does not support the %s date part", part)
		}
		return bqTemporal{"TIMESTAMP", time.UnixMicro(tv.t.UnixMicro() + n*fixed).UTC()}, nil
	case "TIME":
		if !isFixed || part == "DAY" {
			return tv, bqQueryErr("TIME_ADD does not support the %s date part", part)
		}
		day := int64(24 * 3600 * 1000000)
		micros := ((tv.t.Sub(time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)).Microseconds()+n*fixed)%day + day) % day
		return bqTemporal{"TIME", time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(micros) * time.Microsecond)}, nil
	}
	if isFixed {
		if tv.kind == "DATE" && part != "DAY" {
			return tv, bqQueryErr("DATE_ADD does not support the %s date part", part)
		}
		return bqTemporal{tv.kind, tv.t.Add(time.Duration(n*fixed) * time.Microsecond)}, nil
	}
	t := tv.t
	switch part {
	case "WEEK":
		t = t.AddDate(0, 0, int(n)*7)
	case "MONTH", "QUARTER", "YEAR":
		months := n
		if part == "QUARTER" {
			months *= 3
		}
		if part == "YEAR" {
			months *= 12
		}
		first := time.Date(t.Year(), t.Month(), 1, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC).AddDate(0, int(months), 0)
		last := first.AddDate(0, 1, -1).Day()
		day := t.Day()
		if day > last {
			day = last
		}
		t = time.Date(first.Year(), first.Month(), day, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
	default:
		return tv, bqQueryErr("Unsupported date part %s", part)
	}
	if t.Year() < 1 || t.Year() > 9999 {
		return tv, bqQueryErr("%s_ADD overflows the %s range", tv.kind, tv.kind)
	}
	return bqTemporal{tv.kind, t}, nil
}

func bqFnAdd(sign int64) bqScalarFunc {
	return func(args []driver.Value) (driver.Value, error) {
		if bqAnyNil(args) {
			return nil, nil
		}
		tv, err := bqReadTemporal(args[0])
		if err != nil {
			return nil, err
		}
		n, _, err := bqArgInt(args, 1)
		if err != nil {
			return nil, err
		}
		unit, _, err := bqArgString(args, 2)
		if err != nil {
			return nil, err
		}
		part, _, err := bqParseDatePart(unit)
		if err != nil {
			return nil, err
		}
		out, err := bqAddPart(tv, sign*n, part)
		if err != nil {
			return nil, err
		}
		return out.value(), nil
	}
}

// bqTruncPart truncates a civil time to the start of part.
func bqTruncPart(c time.Time, part string, weekStart time.Weekday) time.Time {
	switch part {
	case "MICROSECOND":
		return c.Truncate(time.Microsecond)
	case "MILLISECOND":
		return c.Truncate(time.Millisecond)
	case "SECOND":
		return c.Truncate(time.Second)
	case "MINUTE":
		return time.Date(c.Year(), c.Month(), c.Day(), c.Hour(), c.Minute(), 0, 0, time.UTC)
	case "HOUR":
		return time.Date(c.Year(), c.Month(), c.Day(), c.Hour(), 0, 0, 0, time.UTC)
	case "DAY":
		return time.Date(c.Year(), c.Month(), c.Day(), 0, 0, 0, 0, time.UTC)
	case "WEEK":
		d := time.Date(c.Year(), c.Month(), c.Day(), 0, 0, 0, 0, time.UTC)
		return d.AddDate(0, 0, -((int(d.Weekday()) - int(weekStart) + 7) % 7))
	case "ISOWEEK":
		d := time.Date(c.Year(), c.Month(), c.Day(), 0, 0, 0, 0, time.UTC)
		return d.AddDate(0, 0, -((int(d.Weekday()) + 6) % 7))
	case "MONTH":
		return time.Date(c.Year(), c.Month(), 1, 0, 0, 0, 0, time.UTC)
	case "QUARTER":
		return time.Date(c.Year(), time.Month((int(c.Month())-1)/3*3+1), 1, 0, 0, 0, 0, time.UTC)
	case "YEAR":
		return time.Date(c.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
	case "ISOYEAR":
		y, _ := c.ISOWeek()
		return bqISOYearStart(y)
	}
	return c
}

func bqFnTrunc(args []driver.Value) (driver.Value, error) {
	if len(args) < 2 || len(args) > 3 {
		return nil, bqQueryErr("No matching signature for a TRUNC function for argument count %d", len(args))
	}
	if args[0] == nil || args[1] == nil {
		return nil, nil
	}
	tv, err := bqReadTemporal(args[0])
	if err != nil {
		return nil, err
	}
	partText, _, err := bqArgString(args, 1)
	if err != nil {
		return nil, err
	}
	part, weekStart, err := bqParseDatePart(partText)
	if err != nil {
		return nil, err
	}
	loc, err := bqArgZone(args, 2)
	if err != nil {
		return nil, err
	}
	if len(args) == 3 && tv.kind != "TIMESTAMP" {
		return nil, bqQueryErr("A time zone argument applies to a TIMESTAMP, not a %s", tv.kind)
	}
	if _, fixed := bqUnitMicros(part); tv.kind == "DATE" && fixed && part != "DAY" || tv.kind == "TIME" && (!fixed || part == "DAY") {
		return nil, bqQueryErr("%s_TRUNC does not support the %s date part", tv.kind, part)
	}
	return tv.fromCivil(bqTruncPart(tv.civil(loc), part, weekStart), loc).value(), nil
}

// bqDiffPart counts the part boundaries from start to end: for a TIMESTAMP
// whole fixed-length units, for a civil value the number of truncated units
// between them.
func bqDiffPart(end, start bqTemporal, part string, weekStart time.Weekday) (int64, error) {
	fixed, isFixed := bqUnitMicros(part)
	if end.kind == "TIMESTAMP" || start.kind == "TIMESTAMP" {
		if !isFixed {
			return 0, bqQueryErr("TIMESTAMP_DIFF does not support the %s date part", part)
		}
		e, s := end.civil(time.UTC), start.civil(time.UTC)
		if end.kind == "TIMESTAMP" {
			e = end.t
		}
		if start.kind == "TIMESTAMP" {
			s = start.t
		}
		return (e.UnixMicro() - s.UnixMicro()) / fixed, nil
	}
	if end.kind == "DATE" && isFixed && part != "DAY" || end.kind == "TIME" && (!isFixed || part == "DAY") {
		return 0, bqQueryErr("%s_DIFF does not support the %s date part", end.kind, part)
	}
	e, s := bqTruncPart(end.t, part, weekStart), bqTruncPart(start.t, part, weekStart)
	switch part {
	case "MONTH", "QUARTER", "YEAR":
		months := int64(e.Year()-s.Year())*12 + int64(e.Month()-s.Month())
		switch part {
		case "QUARTER":
			return months / 3, nil
		case "YEAR":
			return months / 12, nil
		}
		return months, nil
	case "ISOYEAR":
		ey, _ := end.t.ISOWeek()
		sy, _ := start.t.ISOWeek()
		return int64(ey - sy), nil
	case "WEEK", "ISOWEEK":
		return int64(e.Sub(s).Hours() / 24 / 7), nil
	case "DAY":
		return int64(e.Sub(s).Hours() / 24), nil
	}
	return (e.UnixMicro() - s.UnixMicro()) / fixed, nil
}

func bqFnDiff(args []driver.Value) (driver.Value, error) {
	if bqAnyNil(args) {
		return nil, nil
	}
	end, err := bqReadTemporal(args[0])
	if err != nil {
		return nil, err
	}
	start, err := bqReadTemporal(args[1])
	if err != nil {
		return nil, err
	}
	partText, _, err := bqArgString(args, 2)
	if err != nil {
		return nil, err
	}
	part, weekStart, err := bqParseDatePart(partText)
	if err != nil {
		return nil, err
	}
	return bqDiffPart(end, start, part, weekStart)
}

func bqFnExtract(args []driver.Value) (driver.Value, error) {
	if len(args) < 2 || len(args) > 3 {
		return nil, bqQueryErr("No matching signature for function EXTRACT for argument count %d", len(args))
	}
	if args[0] == nil || args[1] == nil {
		return nil, nil
	}
	partText, _, err := bqArgString(args, 0)
	if err != nil {
		return nil, err
	}
	part, weekStart, err := bqParseDatePart(partText)
	if err != nil {
		return nil, err
	}
	tv, err := bqReadTemporal(args[1])
	if err != nil {
		return nil, err
	}
	loc, err := bqArgZone(args, 2)
	if err != nil {
		return nil, err
	}
	if len(args) == 3 && tv.kind != "TIMESTAMP" {
		return nil, bqQueryErr("AT TIME ZONE applies to a TIMESTAMP, not a %s", tv.kind)
	}
	c := tv.civil(loc)
	_, fixed := bqUnitMicros(part)
	if tv.kind == "DATE" && (fixed && part != "DAY" || part == "TIME" || part == "DATETIME") || tv.kind == "TIME" && (!fixed || part == "DAY") {
		return nil, bqQueryErr("EXTRACT from %s does not support the %s part", tv.kind, part)
	}
	switch part {
	case "MICROSECOND":
		return int64(c.Nanosecond() / 1000), nil
	case "MILLISECOND":
		return int64(c.Nanosecond() / 1000000), nil
	case "SECOND":
		return int64(c.Second()), nil
	case "MINUTE":
		return int64(c.Minute()), nil
	case "HOUR":
		return int64(c.Hour()), nil
	case "DAYOFWEEK":
		return int64(c.Weekday()) + 1, nil
	case "DAY":
		return int64(c.Day()), nil
	case "DAYOFYEAR":
		return int64(c.YearDay()), nil
	case "WEEK":
		return int64(bqWeekOfYear(c, weekStart)), nil
	case "ISOWEEK":
		_, w := c.ISOWeek()
		return int64(w), nil
	case "MONTH":
		return int64(c.Month()), nil
	case "QUARTER":
		return int64((int(c.Month()) + 2) / 3), nil
	case "YEAR":
		return int64(c.Year()), nil
	case "ISOYEAR":
		y, _ := c.ISOWeek()
		return int64(y), nil
	case "DATE":
		return bqTemporal{"DATE", bqTruncPart(c, "DAY", weekStart)}.value(), nil
	case "DATETIME":
		return bqTemporal{"DATETIME", c}.value(), nil
	case "TIME":
		return bqTemporal{"TIME", time.Date(1970, 1, 1, c.Hour(), c.Minute(), c.Second(), c.Nanosecond(), time.UTC)}.value(), nil
	}
	return nil, bqQueryErr("A valid date part is required but found %s", partText)
}

func bqFnLastDay(args []driver.Value) (driver.Value, error) {
	if len(args) == 0 || len(args) > 2 {
		return nil, bqQueryErr("No matching signature for function LAST_DAY for argument count %d", len(args))
	}
	if args[0] == nil {
		return nil, nil
	}
	tv, err := bqReadTemporal(args[0])
	if err != nil {
		return nil, err
	}
	if tv.kind != "DATE" && tv.kind != "DATETIME" {
		return nil, bqQueryErr("No matching signature for function LAST_DAY for argument types: %s", tv.kind)
	}
	part, weekStart := "MONTH", time.Sunday
	if len(args) == 2 {
		text, _, err := bqArgString(args, 1)
		if err != nil {
			return nil, err
		}
		if part, weekStart, err = bqParseDatePart(text); err != nil {
			return nil, err
		}
	}
	start := bqTruncPart(tv.t, part, weekStart)
	var next time.Time
	switch part {
	case "YEAR":
		next = start.AddDate(1, 0, 0)
	case "QUARTER":
		next = start.AddDate(0, 3, 0)
	case "MONTH":
		next = start.AddDate(0, 1, 0)
	case "WEEK", "ISOWEEK":
		next = start.AddDate(0, 0, 7)
	case "ISOYEAR":
		y, _ := tv.t.ISOWeek()
		next = bqISOYearStart(y + 1)
	default:
		return nil, bqQueryErr("LAST_DAY does not support the %s date part", part)
	}
	last := next.AddDate(0, 0, -1)
	if last.Year() > 9999 {
		return nil, bqQueryErr("LAST_DAY overflows the DATE range")
	}
	return bqTemporal{"DATE", last}.value(), nil
}

func bqFnFormat(args []driver.Value) (driver.Value, error) {
	if len(args) < 2 || len(args) > 3 {
		return nil, bqQueryErr("No matching signature for a FORMAT function for argument count %d", len(args))
	}
	if args[0] == nil || args[1] == nil {
		return nil, nil
	}
	format, _, err := bqArgString(args, 0)
	if err != nil {
		return nil, err
	}
	tv, err := bqReadTemporal(args[1])
	if err != nil {
		return nil, err
	}
	loc, err := bqArgZone(args, 2)
	if err != nil {
		return nil, err
	}
	t := tv.t
	if tv.kind == "TIMESTAMP" {
		t = tv.t.In(loc)
	}
	return bqFormatTime(format, t)
}

func bqFnParse(kind string) bqScalarFunc {
	return func(args []driver.Value) (driver.Value, error) {
		if len(args) < 2 || len(args) > 3 {
			return nil, bqQueryErr("No matching signature for function PARSE_%s for argument count %d", kind, len(args))
		}
		if args[0] == nil || args[1] == nil {
			return nil, nil
		}
		format, _, err := bqArgString(args, 0)
		if err != nil {
			return nil, err
		}
		text, _, err := bqArgString(args, 1)
		if err != nil {
			return nil, err
		}
		loc, err := bqArgZone(args, 2)
		if err != nil {
			return nil, err
		}
		t, err := bqParseTime(format, text, loc)
		if err != nil {
			return nil, err
		}
		switch kind {
		case "TIMESTAMP":
			return bqTemporal{"TIMESTAMP", t.UTC()}.value(), nil
		case "DATE":
			return bqTemporal{"DATE", time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)}.value(), nil
		case "TIME":
			return bqTemporal{"TIME", time.Date(1970, 1, 1, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)}.value(), nil
		}
		return bqTemporal{"DATETIME", time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)}.value(), nil
	}
}

func bqFnTimestampUnits(perUnit int64) bqScalarFunc {
	return func(args []driver.Value) (driver.Value, error) {
		n, ok, err := bqArgInt(args, 0)
		if err != nil || !ok {
			return nil, err
		}
		return n * perUnit, nil
	}
}

func bqFnUnixUnits(perUnit int64) bqScalarFunc {
	return func(args []driver.Value) (driver.Value, error) {
		if args[0] == nil {
			return nil, nil
		}
		tv, err := bqReadTemporal(args[0])
		if err != nil {
			return nil, err
		}
		if tv.kind != "TIMESTAMP" {
			return nil, bqQueryErr("No matching signature for a UNIX function for argument types: %s", tv.kind)
		}
		micros := tv.t.UnixMicro()
		if micros < 0 && micros%perUnit != 0 {
			return micros/perUnit - 1, nil
		}
		return micros / perUnit, nil
	}
}

func bqFnUnixDate(args []driver.Value) (driver.Value, error) {
	if args[0] == nil {
		return nil, nil
	}
	tv, err := bqReadTemporal(args[0])
	if err != nil {
		return nil, err
	}
	if tv.kind != "DATE" {
		return nil, bqQueryErr("No matching signature for function UNIX_DATE for argument types: %s", tv.kind)
	}
	return int64(math.Floor(tv.t.Sub(time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)).Hours() / 24)), nil
}

func bqFnDateFromUnixDate(args []driver.Value) (driver.Value, error) {
	n, ok, err := bqArgInt(args, 0)
	if err != nil || !ok {
		return nil, err
	}
	return bqTemporal{"DATE", time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, int(n))}.value(), nil
}

// bqHintField decodes the static type a rewrite passed beside a value: the
// field schema as JSON, or a bare type name.
func bqHintField(hint string) (BQFieldSchema, bool) {
	hint = strings.TrimSpace(hint)
	if hint == "" {
		return BQFieldSchema{}, false
	}
	if strings.HasPrefix(hint, "{") {
		var f BQFieldSchema
		if err := json.Unmarshal([]byte(hint), &f); err == nil && f.Type != "" {
			return f, true
		}
		return BQFieldSchema{}, false
	}
	f, err := bqParseSQLType(hint)
	if err != nil {
		return BQFieldSchema{}, false
	}
	return f, true
}

// bqCastValue is CAST(v AS target) where src is what the rewrite knew of v's
// type, and otherwise v's SQLite type.
func bqCastValue(v driver.Value, target string, src BQFieldSchema, hasSrc bool) (driver.Value, error) {
	if v == nil {
		return nil, nil
	}
	targetField, err := bqParseSQLType(target)
	if err != nil {
		return nil, err
	}
	if !hasSrc {
		switch x := v.(type) {
		case int64:
			src = BQFieldSchema{Type: "INT64"}
		case float64:
			src = BQFieldSchema{Type: "FLOAT64"}
		case []byte:
			src = BQFieldSchema{Type: "BYTES"}
			v = base64.StdEncoding.EncodeToString(x)
		default:
			src = BQFieldSchema{Type: "STRING"}
		}
	}
	srcType := bqType(src)
	if bqNested(targetField) || bqNested(src) {
		if bqNested(targetField) && bqNested(src) && bqSameSchema([]BQFieldSchema{src}, []BQFieldSchema{targetField}) {
			return v, nil
		}
		return nil, bqQueryErr("Invalid cast from %s to %s", bqSQLTypeName(src), target)
	}
	text := strings.TrimSpace(bqScalarText(v))
	if b, ok := v.([]byte); ok {
		text = string(b)
	}
	switch bqType(targetField) {
	case "STRING":
		switch srcType {
		case "TIMESTAMP":
			tv, err := bqReadTemporal(v)
			if err != nil {
				return nil, err
			}
			return bqTimestampText(tv.t, time.UTC), nil
		case "BOOL":
			if n, ok := v.(int64); ok {
				return strconv.FormatBool(n != 0), nil
			}
			return text, nil
		case "DATETIME":
			return strings.Replace(text, "T", " ", 1), nil
		case "BYTES":
			raw, err := base64.StdEncoding.DecodeString(text)
			if err != nil || !utf8.Valid(raw) {
				return nil, bqQueryErr("Invalid cast of bytes to UTF8 string")
			}
			return string(raw), nil
		case "FLOAT64":
			f, err := bqParseFloat(text)
			if err != nil {
				return nil, bqQueryErr("Bad double value: %s", text)
			}
			return bqFormatFloat(f), nil
		}
		return text, nil
	case "INT64":
		switch srcType {
		case "BOOL":
			if n, ok := v.(int64); ok {
				return n, nil
			}
		case "TIMESTAMP", "DATE", "DATETIME", "TIME", "BYTES":
			return nil, bqQueryErr("Invalid cast from %s to INT64", srcType)
		}
		switch n := v.(type) {
		case int64:
			return n, nil
		case float64:
			if math.IsNaN(n) || math.IsInf(n, 0) || math.Abs(n) >= 1<<63 {
				return nil, bqQueryErr("Bad int64 value: %s", bqFormatFloat(n))
			}
			return int64(math.Round(n)), nil
		}
		if strings.HasPrefix(strings.TrimLeft(text, "+-"), "0x") || strings.HasPrefix(strings.TrimLeft(text, "+-"), "0X") {
			neg := strings.HasPrefix(text, "-")
			n, err := strconv.ParseInt(strings.TrimLeft(text, "+-")[2:], 16, 64)
			if err != nil {
				return nil, bqQueryErr("Bad int64 value: %s", text)
			}
			if neg {
				n = -n
			}
			return n, nil
		}
		if n, err := strconv.ParseInt(text, 10, 64); err == nil {
			return n, nil
		}
		if srcType == "NUMERIC" || srcType == "BIGNUMERIC" {
			f, err := strconv.ParseFloat(text, 64)
			if err == nil {
				return int64(math.Round(f)), nil
			}
		}
		return nil, bqQueryErr("Bad int64 value: %s", text)
	case "FLOAT64":
		switch srcType {
		case "TIMESTAMP", "DATE", "DATETIME", "TIME", "BYTES", "BOOL":
			return nil, bqQueryErr("Invalid cast from %s to FLOAT64", srcType)
		}
		switch n := v.(type) {
		case int64:
			return float64(n), nil
		case float64:
			return n, nil
		}
		f, err := bqParseFloat(text)
		if err != nil {
			return nil, bqQueryErr("Bad double value: %s", text)
		}
		return f, nil
	case "NUMERIC", "BIGNUMERIC":
		switch srcType {
		case "TIMESTAMP", "DATE", "DATETIME", "TIME", "BYTES", "BOOL":
			return nil, bqQueryErr("Invalid cast from %s to %s", srcType, bqType(targetField))
		}
		out, err := bqCanonical(bqType(targetField), text)
		if err != nil {
			return nil, bqQueryErr("Invalid %s value: %s", bqType(targetField), text)
		}
		return out, nil
	case "BOOL":
		switch srcType {
		case "INT64", "BOOL":
			if n, ok := v.(int64); ok {
				if n != 0 {
					return int64(1), nil
				}
				return int64(0), nil
			}
		case "STRING":
			switch strings.ToLower(text) {
			case "true":
				return int64(1), nil
			case "false":
				return int64(0), nil
			}
			return nil, bqQueryErr("Bad bool value: %s", text)
		}
		return nil, bqQueryErr("Invalid cast from %s to BOOL", srcType)
	case "BYTES":
		switch srcType {
		case "BYTES":
			return text, nil
		case "STRING":
			return base64.StdEncoding.EncodeToString([]byte(text)), nil
		}
		return nil, bqQueryErr("Invalid cast from %s to BYTES", srcType)
	case "TIMESTAMP", "DATE", "DATETIME", "TIME":
		if srcType == "INT64" || srcType == "FLOAT64" || srcType == "BOOL" || srcType == "BYTES" || srcType == "NUMERIC" || srcType == "BIGNUMERIC" {
			if _, isTS := v.(int64); !isTS || srcType != "TIMESTAMP" {
				return nil, bqQueryErr("Invalid cast from %s to %s", srcType, bqType(targetField))
			}
		}
		fn := map[string]bqScalarFunc{"TIMESTAMP": bqFnTimestamp, "DATE": bqFnDate, "DATETIME": bqFnDatetime, "TIME": bqFnTime}[bqType(targetField)]
		if srcType == "STRING" && bqType(targetField) == "DATETIME" {
			tv, err := bqReadTemporal(v)
			if err != nil || tv.kind == "TIME" {
				return nil, bqQueryErr("Invalid datetime string \"%s\"", text)
			}
			if tv.kind == "TIMESTAMP" {
				if bqZoneSuffixRE.MatchString(text) {
					return nil, bqQueryErr("Invalid datetime string \"%s\"", text)
				}
			}
		}
		return fn([]driver.Value{v})
	case "JSON":
		if srcType == "JSON" {
			return text, nil
		}
		return nil, bqQueryErr("Invalid cast from %s to JSON", srcType)
	case "GEOGRAPHY", "INTERVAL", "RANGE":
		return nil, bqQueryErr("Invalid cast from %s to %s", srcType, bqType(targetField))
	}
	return nil, bqQueryErr("Invalid cast to %s", target)
}

func bqFnCast(args []driver.Value) (driver.Value, error) {
	target, _, err := bqArgString(args, 1)
	if err != nil {
		return nil, err
	}
	hint, _, err := bqArgString(args, 2)
	if err != nil {
		return nil, err
	}
	src, hasSrc := bqHintField(hint)
	return bqCastValue(args[0], target, src, hasSrc)
}

func bqArgFloat(v driver.Value) (float64, bool) {
	switch n := v.(type) {
	case int64:
		return float64(n), true
	case float64:
		return n, true
	case string:
		f, err := bqParseFloat(strings.TrimSpace(n))
		return f, err == nil
	}
	return 0, false
}

func bqFnSafeDivide(args []driver.Value) (driver.Value, error) {
	if bqAnyNil(args) {
		return nil, nil
	}
	x, okX := bqArgFloat(args[0])
	y, okY := bqArgFloat(args[1])
	if !okX || !okY {
		return nil, bqQueryErr("No matching signature for function SAFE_DIVIDE for argument types: %s, %s", bqSQLiteTypeName(args[0]), bqSQLiteTypeName(args[1]))
	}
	if y == 0 {
		return nil, nil
	}
	return x / y, nil
}

func bqFnIEEEDivide(args []driver.Value) (driver.Value, error) {
	if bqAnyNil(args) {
		return nil, nil
	}
	x, okX := bqArgFloat(args[0])
	y, okY := bqArgFloat(args[1])
	if !okX || !okY {
		return nil, bqQueryErr("No matching signature for function IEEE_DIVIDE for argument types: %s, %s", bqSQLiteTypeName(args[0]), bqSQLiteTypeName(args[1]))
	}
	return x / y, nil
}

func bqFnDiv(args []driver.Value) (driver.Value, error) {
	if bqAnyNil(args) {
		return nil, nil
	}
	x, _, err := bqArgInt(args, 0)
	if err != nil {
		return nil, err
	}
	y, _, err := bqArgInt(args, 1)
	if err != nil {
		return nil, err
	}
	if y == 0 {
		return nil, bqQueryErr("division by zero: %d / 0", x)
	}
	if x == math.MinInt64 && y == -1 {
		return nil, bqQueryErr("int64 overflow: %d / %d", x, y)
	}
	return x / y, nil
}

func bqFnMod(args []driver.Value) (driver.Value, error) {
	if bqAnyNil(args) {
		return nil, nil
	}
	x, _, err := bqArgInt(args, 0)
	if err != nil {
		return nil, err
	}
	y, _, err := bqArgInt(args, 1)
	if err != nil {
		return nil, err
	}
	if y == 0 {
		return nil, bqQueryErr("division by zero: MOD(%d, 0)", x)
	}
	if y == -1 {
		return int64(0), nil
	}
	return x % y, nil
}

func bqFnSafeArith(op byte) bqScalarFunc {
	return func(args []driver.Value) (driver.Value, error) {
		if bqAnyNil(args) {
			return nil, nil
		}
		x, xInt := args[0].(int64)
		y, yInt := args[1].(int64)
		if xInt && yInt {
			var r int64
			switch op {
			case '+':
				r = x + y
				if (r > x) != (y > 0) {
					return nil, nil
				}
			case '-':
				r = x - y
				if (r < x) != (y > 0) {
					return nil, nil
				}
			case '*':
				r = x * y
				if x != 0 && (r/x != y || (x == -1 && y == math.MinInt64) || (y == -1 && x == math.MinInt64)) {
					return nil, nil
				}
			}
			return r, nil
		}
		fx, okX := bqArgFloat(args[0])
		fy, okY := bqArgFloat(args[1])
		if !okX || !okY {
			return nil, bqQueryErr("No matching signature for a SAFE arithmetic function for argument types: %s, %s", bqSQLiteTypeName(args[0]), bqSQLiteTypeName(args[1]))
		}
		var r float64
		switch op {
		case '+':
			r = fx + fy
		case '-':
			r = fx - fy
		case '*':
			r = fx * fy
		}
		if math.IsInf(r, 0) && !math.IsInf(fx, 0) && !math.IsInf(fy, 0) {
			return nil, nil
		}
		return r, nil
	}
}

func bqFnSafeNegate(args []driver.Value) (driver.Value, error) {
	switch n := args[0].(type) {
	case nil:
		return nil, nil
	case int64:
		if n == math.MinInt64 {
			return nil, nil
		}
		return -n, nil
	case float64:
		return -n, nil
	}
	return nil, bqQueryErr("No matching signature for function SAFE_NEGATE for argument types: %s", bqSQLiteTypeName(args[0]))
}

func bqFnTrunc2(args []driver.Value) (driver.Value, error) {
	if len(args) == 0 || len(args) > 2 || bqAnyNil(args) {
		return nil, nil
	}
	if n, ok := args[0].(int64); ok {
		return n, nil
	}
	x, ok := bqArgFloat(args[0])
	if !ok {
		return nil, bqQueryErr("No matching signature for function TRUNC for argument types: %s", bqSQLiteTypeName(args[0]))
	}
	digits := int64(0)
	if len(args) == 2 {
		var err error
		if digits, _, err = bqArgInt(args, 1); err != nil {
			return nil, err
		}
	}
	scale := math.Pow(10, float64(digits))
	return math.Trunc(x*scale) / scale, nil
}

func bqFnConcat(args []driver.Value) (driver.Value, error) {
	if bqAnyNil(args) {
		return nil, nil
	}
	var b strings.Builder
	for _, a := range args {
		switch v := a.(type) {
		case string:
			b.WriteString(v)
		case []byte:
			b.Write(v)
		default:
			return nil, bqQueryErr("No matching signature for function CONCAT for argument types: %s", bqSQLiteTypeName(a))
		}
	}
	return b.String(), nil
}

// bqArgArray decodes a JSON array argument.
func bqArgArray(v driver.Value, fn string) ([]any, error) {
	text, ok := v.(string)
	if !ok {
		if b, isBytes := v.([]byte); isBytes {
			text, ok = string(b), true
		}
	}
	var items []any
	if !ok || json.Unmarshal([]byte(text), &items) != nil || !strings.HasPrefix(strings.TrimSpace(text), "[") {
		return nil, bqQueryErr("No matching signature for function %s for argument types: %s", fn, bqSQLiteTypeName(v))
	}
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	items = nil
	if err := dec.Decode(&items); err != nil {
		return nil, bqQueryErr("No matching signature for function %s for argument types: %s", fn, bqSQLiteTypeName(v))
	}
	return items, nil
}

func bqJSONText(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(b)
}

func bqFnArrayLength(args []driver.Value) (driver.Value, error) {
	if args[0] == nil {
		return nil, nil
	}
	items, err := bqArgArray(args[0], "ARRAY_LENGTH")
	if err != nil {
		return nil, err
	}
	return int64(len(items)), nil
}

func bqFnArrayToString(args []driver.Value) (driver.Value, error) {
	if len(args) < 2 || len(args) > 3 {
		return nil, bqQueryErr("No matching signature for function ARRAY_TO_STRING for argument count %d", len(args))
	}
	if args[0] == nil || args[1] == nil {
		return nil, nil
	}
	items, err := bqArgArray(args[0], "ARRAY_TO_STRING")
	if err != nil {
		return nil, err
	}
	delim, _, err := bqArgString(args, 1)
	if err != nil {
		return nil, err
	}
	nullText, hasNull, err := bqArgString(args, 2)
	if err != nil {
		return nil, err
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		switch x := item.(type) {
		case nil:
			if hasNull {
				parts = append(parts, nullText)
			}
		case string:
			parts = append(parts, x)
		default:
			return nil, bqQueryErr("No matching signature for function ARRAY_TO_STRING: the array holds %T elements, not STRING", item)
		}
	}
	return strings.Join(parts, delim), nil
}

func bqFnArrayConcat(args []driver.Value) (driver.Value, error) {
	if len(args) == 0 {
		return nil, bqQueryErr("No matching signature for function ARRAY_CONCAT for argument count 0")
	}
	if bqAnyNil(args) {
		return nil, nil
	}
	var out []any
	for _, a := range args {
		items, err := bqArgArray(a, "ARRAY_CONCAT")
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
	}
	if out == nil {
		out = []any{}
	}
	return bqJSONText(out), nil
}

func bqFnArrayReverse(args []driver.Value) (driver.Value, error) {
	if args[0] == nil {
		return nil, nil
	}
	items, err := bqArgArray(args[0], "ARRAY_REVERSE")
	if err != nil {
		return nil, err
	}
	for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
		items[i], items[j] = items[j], items[i]
	}
	if items == nil {
		items = []any{}
	}
	return bqJSONText(items), nil
}

// bqGenerateArrayLimit is BigQuery's hard limit on a generated array.
const bqGenerateArrayLimit = 1048576

func bqFnGenerateArray(args []driver.Value) (driver.Value, error) {
	if len(args) < 2 || len(args) > 3 {
		return nil, bqQueryErr("No matching signature for function GENERATE_ARRAY for argument count %d", len(args))
	}
	if bqAnyNil(args) {
		return nil, nil
	}
	integers := true
	values := make([]float64, len(args))
	for i, a := range args {
		f, ok := bqArgFloat(a)
		if !ok {
			return nil, bqQueryErr("No matching signature for function GENERATE_ARRAY for argument types: %s", bqSQLiteTypeName(a))
		}
		if _, isInt := a.(int64); !isInt {
			integers = false
		}
		if math.IsNaN(f) {
			return nil, bqQueryErr("GENERATE_ARRAY does not accept NaN")
		}
		values[i] = f
	}
	step := 1.0
	if len(args) == 3 {
		step = values[2]
	}
	if step == 0 {
		return nil, bqQueryErr("Sequence step cannot be 0.")
	}
	out := []any{}
	for v := values[0]; (step > 0 && v <= values[1]) || (step < 0 && v >= values[1]); v += step {
		if len(out) >= bqGenerateArrayLimit {
			return nil, bqQueryErr("GENERATE_ARRAY produced too many elements; the limit is %d", bqGenerateArrayLimit)
		}
		if integers {
			out = append(out, int64(v))
		} else {
			out = append(out, v)
		}
	}
	return bqJSONText(out), nil
}

// bqFnArrayAt is the array subscript operator: args are the array, the index
// and the position keyword.
func bqFnArrayAt(args []driver.Value) (driver.Value, error) {
	if args[0] == nil || args[1] == nil {
		return nil, nil
	}
	items, err := bqArgArray(args[0], "the array subscript operator")
	if err != nil {
		return nil, err
	}
	index, _, err := bqArgInt(args, 1)
	if err != nil {
		return nil, err
	}
	keyword, _, err := bqArgString(args, 2)
	if err != nil {
		return nil, err
	}
	keyword = strings.ToUpper(keyword)
	position := index
	if strings.HasSuffix(keyword, "ORDINAL") {
		position = index - 1
	}
	if position < 0 || position >= int64(len(items)) {
		if strings.HasPrefix(keyword, "SAFE_") {
			return nil, nil
		}
		return nil, bqQueryErr("Array index %d is out of bounds (overflow)", index)
	}
	return bqJSONScalar(items[position]), nil
}

// bqJSONScalar is a decoded JSON element as a SQLite value: a number as
// INTEGER or REAL, a bool as 0 or 1, a nested value as its JSON text.
func bqJSONScalar(v any) driver.Value {
	switch x := v.(type) {
	case nil:
		return nil
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return n
		}
		f, _ := x.Float64()
		return f
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1<<53 {
			return int64(x)
		}
		return x
	case string:
		return x
	case bool:
		if x {
			return int64(1)
		}
		return int64(0)
	}
	return bqJSONText(v)
}

func bqFnStartsWith(args []driver.Value) (driver.Value, error) {
	if bqAnyNil(args) {
		return nil, nil
	}
	v, _, err := bqArgString(args, 0)
	if err != nil {
		return nil, err
	}
	p, _, err := bqArgString(args, 1)
	if err != nil {
		return nil, err
	}
	return bqBool(strings.HasPrefix(v, p)), nil
}

func bqFnEndsWith(args []driver.Value) (driver.Value, error) {
	if bqAnyNil(args) {
		return nil, nil
	}
	v, _, err := bqArgString(args, 0)
	if err != nil {
		return nil, err
	}
	p, _, err := bqArgString(args, 1)
	if err != nil {
		return nil, err
	}
	return bqBool(strings.HasSuffix(v, p)), nil
}

func bqBool(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

var (
	bqRegexpCache   = map[string]*regexp.Regexp{}
	bqRegexpCacheMu sync.Mutex
)

func bqRegexp(pattern string) (*regexp.Regexp, error) {
	bqRegexpCacheMu.Lock()
	defer bqRegexpCacheMu.Unlock()
	if re, ok := bqRegexpCache[pattern]; ok {
		return re, nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, bqQueryErr("Cannot parse regular expression: %s", err.Error())
	}
	bqRegexpCache[pattern] = re
	return re, nil
}

func bqFnRegexpContains(args []driver.Value) (driver.Value, error) {
	if bqAnyNil(args) {
		return nil, nil
	}
	v, _, err := bqArgString(args, 0)
	if err != nil {
		return nil, err
	}
	pattern, _, err := bqArgString(args, 1)
	if err != nil {
		return nil, err
	}
	re, err := bqRegexp(pattern)
	if err != nil {
		return nil, err
	}
	return bqBool(re.MatchString(v)), nil
}

func bqFnRegexpExtract(args []driver.Value) (driver.Value, error) {
	if len(args) < 2 || len(args) > 4 {
		return nil, bqQueryErr("No matching signature for function REGEXP_EXTRACT for argument count %d", len(args))
	}
	if bqAnyNil(args) {
		return nil, nil
	}
	v, _, err := bqArgString(args, 0)
	if err != nil {
		return nil, err
	}
	pattern, _, err := bqArgString(args, 1)
	if err != nil {
		return nil, err
	}
	re, err := bqRegexp(pattern)
	if err != nil {
		return nil, err
	}
	if re.NumSubexp() > 1 {
		return nil, bqQueryErr("Regular expressions passed into extraction functions must not have more than 1 capturing group")
	}
	position, occurrence := int64(1), int64(1)
	if len(args) >= 3 {
		if position, _, err = bqArgInt(args, 2); err != nil {
			return nil, err
		}
		if position < 1 {
			return nil, bqQueryErr("Position must be positive: %d", position)
		}
	}
	if len(args) == 4 {
		if occurrence, _, err = bqArgInt(args, 3); err != nil {
			return nil, err
		}
		if occurrence < 1 {
			return nil, bqQueryErr("Occurrence must be positive: %d", occurrence)
		}
	}
	runes := []rune(v)
	if position > int64(len(runes)) {
		return nil, nil
	}
	rest := string(runes[position-1:])
	matches := re.FindAllStringSubmatchIndex(rest, -1)
	if int64(len(matches)) < occurrence {
		return nil, nil
	}
	m := matches[occurrence-1]
	if re.NumSubexp() == 1 {
		if m[2] < 0 {
			return nil, nil
		}
		return rest[m[2]:m[3]], nil
	}
	return rest[m[0]:m[1]], nil
}

func bqFnRegexpExtractAll(args []driver.Value) (driver.Value, error) {
	if bqAnyNil(args) {
		return nil, nil
	}
	v, _, err := bqArgString(args, 0)
	if err != nil {
		return nil, err
	}
	pattern, _, err := bqArgString(args, 1)
	if err != nil {
		return nil, err
	}
	re, err := bqRegexp(pattern)
	if err != nil {
		return nil, err
	}
	if re.NumSubexp() > 1 {
		return nil, bqQueryErr("Regular expressions passed into extraction functions must not have more than 1 capturing group")
	}
	out := []any{}
	for _, m := range re.FindAllStringSubmatch(v, -1) {
		if re.NumSubexp() == 1 {
			out = append(out, m[1])
		} else {
			out = append(out, m[0])
		}
	}
	return bqJSONText(out), nil
}

// bqGoReplacement turns a GoogleSQL replacement, where \1 names a group and
// \\ a backslash, into Go's ${1} form.
func bqGoReplacement(replacement string) string {
	var b strings.Builder
	rs := []rune(replacement)
	for i := 0; i < len(rs); i++ {
		switch {
		case rs[i] == '$':
			b.WriteString("$$")
		case rs[i] == '\\' && i+1 < len(rs) && rs[i+1] >= '0' && rs[i+1] <= '9':
			fmt.Fprintf(&b, "${%c}", rs[i+1])
			i++
		case rs[i] == '\\' && i+1 < len(rs) && rs[i+1] == '\\':
			b.WriteByte('\\')
			i++
		default:
			b.WriteRune(rs[i])
		}
	}
	return b.String()
}

func bqFnRegexpReplace(args []driver.Value) (driver.Value, error) {
	if bqAnyNil(args) {
		return nil, nil
	}
	v, _, err := bqArgString(args, 0)
	if err != nil {
		return nil, err
	}
	pattern, _, err := bqArgString(args, 1)
	if err != nil {
		return nil, err
	}
	replacement, _, err := bqArgString(args, 2)
	if err != nil {
		return nil, err
	}
	re, err := bqRegexp(pattern)
	if err != nil {
		return nil, err
	}
	return re.ReplaceAllString(v, bqGoReplacement(replacement)), nil
}

func bqFnSplit(args []driver.Value) (driver.Value, error) {
	if len(args) == 0 || len(args) > 2 {
		return nil, bqQueryErr("No matching signature for function SPLIT for argument count %d", len(args))
	}
	if bqAnyNil(args) {
		return nil, nil
	}
	v, _, err := bqArgString(args, 0)
	if err != nil {
		return nil, err
	}
	delim := ","
	if len(args) == 2 {
		if delim, _, err = bqArgString(args, 1); err != nil {
			return nil, err
		}
	}
	var parts []string
	if delim == "" {
		for _, r := range v {
			parts = append(parts, string(r))
		}
	} else {
		parts = strings.Split(v, delim)
	}
	out := make([]any, len(parts))
	for i, p := range parts {
		out[i] = p
	}
	return bqJSONText(out), nil
}

func bqFnCharLength(args []driver.Value) (driver.Value, error) {
	v, ok, err := bqArgString(args, 0)
	if err != nil || !ok {
		return nil, err
	}
	return int64(utf8.RuneCountInString(v)), nil
}

func bqFnByteLength(args []driver.Value) (driver.Value, error) {
	v, ok, err := bqArgString(args, 0)
	if err != nil || !ok {
		return nil, err
	}
	return int64(len(v)), nil
}

func bqFnStrpos(args []driver.Value) (driver.Value, error) {
	if bqAnyNil(args) {
		return nil, nil
	}
	v, _, err := bqArgString(args, 0)
	if err != nil {
		return nil, err
	}
	sub, _, err := bqArgString(args, 1)
	if err != nil {
		return nil, err
	}
	i := strings.Index(v, sub)
	if i < 0 {
		return int64(0), nil
	}
	return int64(utf8.RuneCountInString(v[:i])) + 1, nil
}

func bqFnRepeat(args []driver.Value) (driver.Value, error) {
	if bqAnyNil(args) {
		return nil, nil
	}
	v, _, err := bqArgString(args, 0)
	if err != nil {
		return nil, err
	}
	n, _, err := bqArgInt(args, 1)
	if err != nil {
		return nil, err
	}
	if n < 0 {
		return nil, bqQueryErr("Second argument must be non-negative")
	}
	return strings.Repeat(v, int(n)), nil
}

func bqFnReverse(args []driver.Value) (driver.Value, error) {
	v, ok, err := bqArgString(args, 0)
	if err != nil || !ok {
		return nil, err
	}
	rs := []rune(v)
	for i, j := 0, len(rs)-1; i < j; i, j = i+1, j-1 {
		rs[i], rs[j] = rs[j], rs[i]
	}
	return string(rs), nil
}

func bqFnPad(left bool) bqScalarFunc {
	return func(args []driver.Value) (driver.Value, error) {
		if len(args) < 2 || len(args) > 3 {
			return nil, bqQueryErr("No matching signature for a PAD function for argument count %d", len(args))
		}
		if bqAnyNil(args) {
			return nil, nil
		}
		v, _, err := bqArgString(args, 0)
		if err != nil {
			return nil, err
		}
		length, _, err := bqArgInt(args, 1)
		if err != nil {
			return nil, err
		}
		pattern := " "
		if len(args) == 3 {
			if pattern, _, err = bqArgString(args, 2); err != nil {
				return nil, err
			}
		}
		if length < 0 {
			return nil, bqQueryErr("Second argument must be non-negative")
		}
		if pattern == "" {
			return nil, bqQueryErr("Third argument (pad pattern) must be non-empty")
		}
		rs := []rune(v)
		if int64(len(rs)) >= length {
			return string(rs[:length]), nil
		}
		pad := []rune(strings.Repeat(pattern, int(length)))[:length-int64(len(rs))]
		if left {
			return string(pad) + v, nil
		}
		return v + string(pad), nil
	}
}

func bqFnInitcap(args []driver.Value) (driver.Value, error) {
	if len(args) == 0 || len(args) > 2 || bqAnyNil(args) {
		return nil, nil
	}
	v, _, err := bqArgString(args, 0)
	if err != nil {
		return nil, err
	}
	delims := " \t\n\r\f\v[](){}/|\\<>!?@\"^#$&~_,.:;*%+-"
	if len(args) == 2 {
		if delims, _, err = bqArgString(args, 1); err != nil {
			return nil, err
		}
	}
	var b strings.Builder
	start := true
	for _, r := range v {
		if strings.ContainsRune(delims, r) {
			b.WriteRune(r)
			start = true
			continue
		}
		if start {
			b.WriteRune(unicode.ToUpper(r))
		} else {
			b.WriteRune(unicode.ToLower(r))
		}
		start = false
	}
	return b.String(), nil
}

// bqArgBytes decodes a BYTES argument from its stored base64 form.
func bqArgBytes(args []driver.Value, i int, fn string) ([]byte, bool, error) {
	if args[i] == nil {
		return nil, false, nil
	}
	if b, ok := args[i].([]byte); ok {
		return b, true, nil
	}
	s, ok := args[i].(string)
	if !ok {
		return nil, false, bqQueryErr("No matching signature for function %s for argument types: %s", fn, bqSQLiteTypeName(args[i]))
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, false, bqQueryErr("No matching signature for function %s for argument types: STRING", fn)
	}
	return b, true, nil
}

func bqFnToHex(args []driver.Value) (driver.Value, error) {
	b, ok, err := bqArgBytes(args, 0, "TO_HEX")
	if err != nil || !ok {
		return nil, err
	}
	return hex.EncodeToString(b), nil
}

func bqFnFromHex(args []driver.Value) (driver.Value, error) {
	v, ok, err := bqArgString(args, 0)
	if err != nil || !ok {
		return nil, err
	}
	if len(v)%2 == 1 {
		v = "0" + v
	}
	b, err := hex.DecodeString(v)
	if err != nil {
		return nil, bqQueryErr("Failed to decode invalid hexadecimal string")
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

func bqFnToBase64(args []driver.Value) (driver.Value, error) {
	b, ok, err := bqArgBytes(args, 0, "TO_BASE64")
	if err != nil || !ok {
		return nil, err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

func bqFnFromBase64(args []driver.Value) (driver.Value, error) {
	v, ok, err := bqArgString(args, 0)
	if err != nil || !ok {
		return nil, err
	}
	b, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		if b, err = base64.RawStdEncoding.DecodeString(v); err != nil {
			return nil, bqQueryErr("Failed to decode invalid base64 string")
		}
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

func bqFnFarmFingerprint(args []driver.Value) (driver.Value, error) {
	switch v := args[0].(type) {
	case nil:
		return nil, nil
	case string:
		return int64(farm.Fingerprint64([]byte(v))), nil
	case []byte:
		return int64(farm.Fingerprint64(v)), nil
	}
	return nil, bqQueryErr("No matching signature for function FARM_FINGERPRINT for argument types: %s", bqSQLiteTypeName(args[0]))
}

// bqFnHash computes a digest of a STRING's UTF-8 bytes or of BYTES and
// answers the BYTES in their stored base64 form.
func bqFnHash(sum func([]byte) []byte) bqScalarFunc {
	return func(args []driver.Value) (driver.Value, error) {
		switch v := args[0].(type) {
		case nil:
			return nil, nil
		case string:
			return base64.StdEncoding.EncodeToString(sum([]byte(v))), nil
		case []byte:
			return base64.StdEncoding.EncodeToString(sum(v)), nil
		}
		return nil, bqQueryErr("No matching signature for a hash function for argument types: %s", bqSQLiteTypeName(args[0]))
	}
}

// bqToJSONValue encodes a stored cell under f as TO_JSON_STRING encodes it.
func bqToJSONValue(f BQFieldSchema, v any) any {
	if v == nil {
		return nil
	}
	if bqRepeated(f) {
		items, _ := v.([]any)
		elem := f
		elem.Mode = "NULLABLE"
		out := make([]any, 0, len(items))
		for _, item := range items {
			out = append(out, bqToJSONValue(elem, item))
		}
		return out
	}
	text := bqScalarText(v)
	switch bqType(f) {
	case "STRUCT":
		obj, _ := v.(map[string]any)
		out := bqOrderedObject{}
		for _, sub := range f.Fields {
			out = append(out, bqMember{sub.Name, bqToJSONValue(sub, obj[sub.Name])})
		}
		return out
	case "INT64":
		if n, err := strconv.ParseInt(text, 10, 64); err == nil && n >= -(1<<53) && n <= 1<<53 {
			return json.Number(text)
		}
		return text
	case "NUMERIC", "BIGNUMERIC":
		if n, err := strconv.ParseInt(text, 10, 64); err == nil && n >= -(1<<53) && n <= 1<<53 {
			return json.Number(text)
		}
		return text
	case "FLOAT64":
		if f, err := strconv.ParseFloat(text, 64); err == nil && !math.IsInf(f, 0) && !math.IsNaN(f) {
			return json.Number(bqFormatFloat(f))
		}
		return text
	case "BOOL":
		return text == "true"
	case "TIMESTAMP":
		if micros, err := strconv.ParseInt(text, 10, 64); err == nil {
			t := time.UnixMicro(micros).UTC()
			return t.Format("2006-01-02T15:04:05.999999") + "Z"
		}
		return text
	case "JSON":
		if json.Valid([]byte(text)) {
			return json.RawMessage(text)
		}
		return text
	}
	return text
}

func bqFnToJSONString(args []driver.Value) (driver.Value, error) {
	if len(args) < 2 || len(args) > 3 {
		return nil, bqQueryErr("No matching signature for function TO_JSON_STRING for argument count %d", len(args)-1)
	}
	pretty := false
	if len(args) == 3 {
		if args[2] == nil {
			return nil, nil
		}
		n, _, err := bqArgInt(args, 2)
		if err != nil {
			return nil, err
		}
		pretty = n != 0
	}
	hint, _, err := bqArgString(args, 1)
	if err != nil {
		return nil, err
	}
	f, hasHint := bqHintField(hint)
	var value any
	switch {
	case args[0] == nil:
		value = nil
	case hasHint:
		stored, err := bqResultCell(f, args[0])
		if err != nil {
			return nil, err
		}
		value = bqToJSONValue(f, stored)
	default:
		switch v := args[0].(type) {
		case int64:
			value = bqToJSONValue(BQFieldSchema{Type: "INT64"}, strconv.FormatInt(v, 10))
		case float64:
			value = bqToJSONValue(BQFieldSchema{Type: "FLOAT64"}, bqFormatFloat(v))
		case []byte:
			value = base64.StdEncoding.EncodeToString(v)
		default:
			value = fmt.Sprint(v)
		}
	}
	var out []byte
	if pretty {
		out, err = json.MarshalIndent(value, "", "  ")
	} else {
		out, err = json.Marshal(value)
	}
	if err != nil {
		return nil, err
	}
	return string(out), nil
}

// bqJSONPath walks a JSONPath of the form $.a.b[0]."c.d" over decoded JSON.
func bqJSONPath(root any, path string) (any, bool, error) {
	path = strings.TrimSpace(path)
	for _, mode := range []string{"lax recursive ", "lax "} {
		path = strings.TrimPrefix(path, mode)
	}
	if !strings.HasPrefix(path, "$") {
		return nil, false, bqQueryErr("JSONPath must start with '$': %s", path)
	}
	rs := []rune(path[1:])
	current := root
	for i := 0; i < len(rs); {
		switch rs[i] {
		case '.':
			i++
			var key string
			if i < len(rs) && rs[i] == '"' {
				j := i + 1
				for j < len(rs) && rs[j] != '"' {
					j++
				}
				if j >= len(rs) {
					return nil, false, bqQueryErr("Invalid JSONPath: %s", path)
				}
				key = string(rs[i+1 : j])
				i = j + 1
			} else {
				j := i
				for j < len(rs) && rs[j] != '.' && rs[j] != '[' {
					j++
				}
				key = string(rs[i:j])
				i = j
			}
			obj, ok := current.(map[string]any)
			if !ok {
				return nil, false, nil
			}
			current, ok = obj[key]
			if !ok {
				return nil, false, nil
			}
		case '[':
			j := i + 1
			for j < len(rs) && rs[j] != ']' {
				j++
			}
			if j >= len(rs) {
				return nil, false, bqQueryErr("Invalid JSONPath: %s", path)
			}
			index, err := strconv.Atoi(strings.TrimSpace(string(rs[i+1 : j])))
			if err != nil {
				return nil, false, bqQueryErr("Invalid JSONPath: %s", path)
			}
			i = j + 1
			items, ok := current.([]any)
			if !ok || index < 0 || index >= len(items) {
				return nil, false, nil
			}
			current = items[index]
		default:
			return nil, false, bqQueryErr("Invalid JSONPath: %s", path)
		}
	}
	return current, true, nil
}

func bqArgJSON(args []driver.Value, fn string) (any, string, bool, error) {
	if args[0] == nil {
		return nil, "", false, nil
	}
	text, _, err := bqArgString(args, 0)
	if err != nil {
		return nil, "", false, err
	}
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	var root any
	if err := dec.Decode(&root); err != nil {
		return nil, "", false, bqQueryErr("Invalid JSON input for function %s: %s", fn, text)
	}
	path := "$"
	if len(args) > 1 {
		if args[1] == nil {
			return nil, "", false, nil
		}
		if path, _, err = bqArgString(args, 1); err != nil {
			return nil, "", false, err
		}
	}
	return root, path, true, nil
}

func bqFnJSONValue(args []driver.Value) (driver.Value, error) {
	root, path, ok, err := bqArgJSON(args, "JSON_VALUE")
	if err != nil || !ok {
		return nil, err
	}
	v, found, err := bqJSONPath(root, path)
	if err != nil || !found {
		return nil, err
	}
	switch x := v.(type) {
	case nil, map[string]any, []any:
		return nil, nil
	case json.Number:
		return string(x), nil
	case bool:
		return strconv.FormatBool(x), nil
	case string:
		return x, nil
	}
	return fmt.Sprint(v), nil
}

func bqFnJSONQuery(args []driver.Value) (driver.Value, error) {
	root, path, ok, err := bqArgJSON(args, "JSON_QUERY")
	if err != nil || !ok {
		return nil, err
	}
	v, found, err := bqJSONPath(root, path)
	if err != nil || !found || v == nil {
		return nil, err
	}
	return bqJSONText(v), nil
}

type bqCountIf struct{ n int64 }

func (a *bqCountIf) Step(_ *sqlite.FunctionContext, args []driver.Value) error {
	if n, ok := args[0].(int64); ok && n != 0 {
		a.n++
	}
	return nil
}

func (a *bqCountIf) WindowInverse(_ *sqlite.FunctionContext, args []driver.Value) error {
	if n, ok := args[0].(int64); ok && n != 0 {
		a.n--
	}
	return nil
}

func (a *bqCountIf) WindowValue(*sqlite.FunctionContext) (driver.Value, error) { return a.n, nil }
func (a *bqCountIf) Final(*sqlite.FunctionContext)                             {}

// bqLogical is LOGICAL_AND or LOGICAL_OR: NULL inputs are ignored and an
// empty group answers NULL.
type bqLogical struct {
	and          bool
	trues, total int
}

func (a *bqLogical) Step(_ *sqlite.FunctionContext, args []driver.Value) error {
	if n, ok := args[0].(int64); ok {
		a.total++
		if n != 0 {
			a.trues++
		}
	}
	return nil
}

func (a *bqLogical) WindowInverse(_ *sqlite.FunctionContext, args []driver.Value) error {
	if n, ok := args[0].(int64); ok {
		a.total--
		if n != 0 {
			a.trues--
		}
	}
	return nil
}

func (a *bqLogical) WindowValue(*sqlite.FunctionContext) (driver.Value, error) {
	if a.total == 0 {
		return nil, nil
	}
	if a.and {
		return bqBool(a.trues == a.total), nil
	}
	return bqBool(a.trues > 0), nil
}

func (a *bqLogical) Final(*sqlite.FunctionContext) {}

type bqAnyValue struct {
	values []driver.Value
}

func (a *bqAnyValue) Step(_ *sqlite.FunctionContext, args []driver.Value) error {
	a.values = append(a.values, bqCopyValue(args[0]))
	return nil
}

func (a *bqAnyValue) WindowInverse(*sqlite.FunctionContext, []driver.Value) error {
	if len(a.values) > 0 {
		a.values = a.values[1:]
	}
	return nil
}

func (a *bqAnyValue) WindowValue(*sqlite.FunctionContext) (driver.Value, error) {
	for _, v := range a.values {
		if v != nil {
			return v, nil
		}
	}
	return nil, nil
}

func (a *bqAnyValue) Final(*sqlite.FunctionContext) {}

// bqCopyValue keeps an argument past the call the driver lends it for.
func bqCopyValue(v driver.Value) driver.Value {
	switch x := v.(type) {
	case []byte:
		return append([]byte(nil), x...)
	case string:
		return strings.Clone(x)
	}
	return v
}

// bqArrayAgg is ARRAY_AGG: the elements in the order SQLite feeds them, which
// an ORDER BY inside the call fixes; a NULL element fails the query unless
// IGNORE NULLS dropped it.
type bqArrayAgg struct {
	ignoreNulls bool
	items       []any
	nulls       int
	rows        int
}

func (a *bqArrayAgg) Step(_ *sqlite.FunctionContext, args []driver.Value) error {
	a.rows++
	if args[0] == nil {
		if !a.ignoreNulls {
			a.nulls++
		}
		return nil
	}
	a.items = append(a.items, bqJSONElement(bqCopyValue(args[0])))
	return nil
}

func (a *bqArrayAgg) WindowInverse(_ *sqlite.FunctionContext, args []driver.Value) error {
	a.rows--
	if args[0] == nil {
		if !a.ignoreNulls {
			a.nulls--
		}
		return nil
	}
	if len(a.items) > 0 {
		a.items = a.items[1:]
	}
	return nil
}

func (a *bqArrayAgg) WindowValue(*sqlite.FunctionContext) (driver.Value, error) {
	if a.rows == 0 {
		return nil, nil
	}
	if a.nulls > 0 {
		return nil, fmt.Errorf("%sArray cannot have a null element; error in writing field", bqRuntimeErrPrefix)
	}
	items := a.items
	if items == nil {
		items = []any{}
	}
	return bqJSONText(items), nil
}

func (a *bqArrayAgg) Final(*sqlite.FunctionContext) {}

// bqJSONElement is a SQLite value as a JSON array element: a JSON-typed
// value (an array or struct) nests as JSON, text stays text.
func bqJSONElement(v driver.Value) any {
	switch x := v.(type) {
	case []byte:
		return base64.StdEncoding.EncodeToString(x)
	case string:
		trimmed := strings.TrimSpace(x)
		if (strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, "{")) && json.Valid([]byte(trimmed)) {
			return json.RawMessage(trimmed)
		}
		return x
	}
	return v
}

type bqStringAgg struct {
	parts []string
	delim *string
	rows  int
}

func (a *bqStringAgg) Step(_ *sqlite.FunctionContext, args []driver.Value) error {
	a.rows++
	if len(args) > 1 && a.delim == nil {
		d, ok, err := bqArgString(args, 1)
		if err != nil {
			return err
		}
		if ok {
			a.delim = &d
		}
	}
	if args[0] == nil {
		return nil
	}
	s, _, err := bqArgString(args, 0)
	if err != nil {
		return err
	}
	a.parts = append(a.parts, strings.Clone(s))
	return nil
}

func (a *bqStringAgg) WindowInverse(_ *sqlite.FunctionContext, args []driver.Value) error {
	a.rows--
	if args[0] != nil && len(a.parts) > 0 {
		a.parts = a.parts[1:]
	}
	return nil
}

func (a *bqStringAgg) WindowValue(*sqlite.FunctionContext) (driver.Value, error) {
	if len(a.parts) == 0 {
		return nil, nil
	}
	delim := ","
	if a.delim != nil {
		delim = *a.delim
	}
	return strings.Join(a.parts, delim), nil
}

func (a *bqStringAgg) Final(*sqlite.FunctionContext) {}
