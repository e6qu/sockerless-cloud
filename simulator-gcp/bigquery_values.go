package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// A table stores each scalar cell in the form tabledata.list carries it: a
// string. INT64 is its decimal text, FLOAT64 the shortest text that round-trips,
// BOOL "true" or "false", BYTES standard base64, TIMESTAMP the microseconds since
// the epoch (the form the clients ask for with formatOptions.useInt64Timestamp),
// DATETIME "YYYY-MM-DDTHH:MM:SS[.ffffff]". A RECORD cell is a map of its fields
// and a REPEATED cell a list.

// bqType names a field's type by its GoogleSQL name, folding the legacy
// spellings the API accepts.
func bqType(f BQFieldSchema) string {
	switch t := strings.ToUpper(f.Type); t {
	case "INTEGER":
		return "INT64"
	case "FLOAT":
		return "FLOAT64"
	case "BOOLEAN":
		return "BOOL"
	case "RECORD":
		return "STRUCT"
	default:
		return t
	}
}

func bqRepeated(f BQFieldSchema) bool { return strings.EqualFold(f.Mode, "REPEATED") }

func bqNested(f BQFieldSchema) bool { return bqRepeated(f) || bqType(f) == "STRUCT" }

// bqConvertError is a value the column's type cannot hold.
type bqConvertError struct{ message string }

func (e *bqConvertError) Error() string { return e.message }

func bqConvertErr(format string, args ...any) error {
	return &bqConvertError{fmt.Sprintf(format, args...)}
}

// bqCoerce converts a source value into the form the table stores for f.
// ignoreUnknown drops members of a RECORD value the schema does not name.
func bqCoerce(f BQFieldSchema, raw any, ignoreUnknown bool) (any, error) {
	if raw == nil {
		if bqRepeated(f) {
			return []any{}, nil
		}
		return nil, nil
	}
	if bqRepeated(f) {
		items, ok := raw.([]any)
		if !ok {
			return nil, bqConvertErr("Repeated field must be imported as a JSON array. Field: %s.", f.Name)
		}
		elem := f
		elem.Mode = "NULLABLE"
		out := make([]any, 0, len(items))
		for _, item := range items {
			if item == nil {
				return nil, bqConvertErr("Only optional fields can be set to NULL. Field: %s; Value: NULL", f.Name)
			}
			v, err := bqCoerce(elem, item, ignoreUnknown)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	}
	if _, ok := raw.([]any); ok {
		return nil, bqConvertErr("Array specified for non-repeated field: %s.", f.Name)
	}
	if bqType(f) == "STRUCT" {
		obj, ok := raw.(map[string]any)
		if !ok {
			return nil, bqConvertErr("Cannot convert non-object value to RECORD. Field: %s", f.Name)
		}
		return bqCoerceRecord(f.Name+".", f.Fields, obj, ignoreUnknown)
	}
	if _, ok := raw.(map[string]any); ok && bqType(f) != "JSON" {
		return nil, bqConvertErr("Cannot convert an object to %s. Field: %s", bqType(f), f.Name)
	}
	v, err := bqCanonical(bqType(f), raw)
	if err != nil {
		return nil, bqConvertErr("Could not convert value '%s' to %s. Field: %s", bqScalarText(raw), bqType(f), f.Name)
	}
	return v, nil
}

// bqCoerceRecord converts each named member of obj and checks the REQUIRED
// ones are present.
func bqCoerceRecord(prefix string, fields []BQFieldSchema, obj map[string]any, ignoreUnknown bool) (map[string]any, error) {
	out := make(map[string]any, len(fields))
	known := make(map[string]bool, len(fields))
	for _, f := range fields {
		known[f.Name] = true
		v, err := bqCoerce(f, obj[f.Name], ignoreUnknown)
		if err != nil {
			return nil, err
		}
		if v == nil && strings.EqualFold(f.Mode, "REQUIRED") {
			return nil, bqConvertErr("Missing required field: %s%s.", prefix, f.Name)
		}
		out[f.Name] = v
	}
	if !ignoreUnknown {
		for name := range obj {
			if !known[name] {
				return nil, bqConvertErr("no such field: %s%s.", prefix, name)
			}
		}
	}
	return out, nil
}

// bqScalarText is the text of a scalar source value.
func bqScalarText(raw any) string {
	switch v := raw.(type) {
	case string:
		return v
	case json.Number:
		return string(v)
	case float64:
		return bqFormatFloat(v)
	case float32:
		return bqFormatFloat(float64(v))
	case bool:
		return strconv.FormatBool(v)
	case int:
		return strconv.Itoa(v)
	case int32:
		return strconv.FormatInt(int64(v), 10)
	case int64:
		return strconv.FormatInt(v, 10)
	case []byte:
		return base64.StdEncoding.EncodeToString(v)
	case time.Time:
		return v.UTC().Format(time.RFC3339Nano)
	case nil:
		return ""
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprint(v)
		}
		return string(b)
	}
}

func bqFormatFloat(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

var (
	bqNumericLimit    = new(big.Rat).SetFrac(new(big.Int).Exp(big.NewInt(10), big.NewInt(29), nil), big.NewInt(1))
	bqBigNumericLimit = new(big.Rat).SetFrac(new(big.Int).Exp(big.NewInt(10), big.NewInt(38), nil), big.NewInt(1))
)

// bqCanonical converts a scalar source value to the stored text of typ.
func bqCanonical(typ string, raw any) (any, error) {
	text := strings.TrimSpace(bqScalarText(raw))
	switch typ {
	case "STRING", "GEOGRAPHY", "INTERVAL", "RANGE":
		if s, ok := raw.(string); ok {
			return s, nil
		}
		return bqScalarText(raw), nil
	case "INT64":
		if f, ok := raw.(float64); ok {
			if f != math.Trunc(f) || math.Abs(f) > 1<<63 {
				return nil, strconv.ErrSyntax
			}
			return strconv.FormatInt(int64(f), 10), nil
		}
		n, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return nil, err
		}
		return strconv.FormatInt(n, 10), nil
	case "FLOAT64":
		f, err := bqParseFloat(text)
		if err != nil {
			return nil, err
		}
		return bqFormatFloat(f), nil
	case "NUMERIC", "BIGNUMERIC":
		r, ok := new(big.Rat).SetString(text)
		if !ok {
			return nil, strconv.ErrSyntax
		}
		scale, limit := 9, bqNumericLimit
		if typ == "BIGNUMERIC" {
			scale, limit = 38, bqBigNumericLimit
		}
		if new(big.Rat).Abs(r).Cmp(limit) >= 0 {
			return nil, strconv.ErrRange
		}
		return bqTrimFraction(r.FloatString(scale)), nil
	case "BOOL":
		if b, ok := raw.(bool); ok {
			return strconv.FormatBool(b), nil
		}
		switch strings.ToLower(text) {
		case "true", "t", "yes", "y", "1":
			return "true", nil
		case "false", "f", "no", "n", "0":
			return "false", nil
		}
		return nil, strconv.ErrSyntax
	case "BYTES":
		if b, ok := raw.([]byte); ok {
			return base64.StdEncoding.EncodeToString(b), nil
		}
		b, err := base64.StdEncoding.DecodeString(text)
		if err != nil {
			return nil, err
		}
		return base64.StdEncoding.EncodeToString(b), nil
	case "DATE":
		if t, ok := raw.(time.Time); ok {
			return t.UTC().Format("2006-01-02"), nil
		}
		t, err := time.Parse("2006-1-2", text)
		if err != nil {
			return nil, err
		}
		return t.Format("2006-01-02"), nil
	case "TIME":
		if t, ok := raw.(time.Time); ok {
			return bqFormatClock(t), nil
		}
		t, err := time.Parse("15:04:05.999999999", text)
		if err != nil {
			return nil, err
		}
		return bqFormatClock(t), nil
	case "DATETIME":
		if t, ok := raw.(time.Time); ok {
			return t.UTC().Format("2006-01-02T") + bqFormatClock(t.UTC()), nil
		}
		t, err := bqParseDateTime(text)
		if err != nil {
			return nil, err
		}
		return t.Format("2006-01-02T") + bqFormatClock(t), nil
	case "TIMESTAMP":
		t, err := bqParseTimestamp(raw, text)
		if err != nil {
			return nil, err
		}
		return strconv.FormatInt(t.UnixMicro(), 10), nil
	case "JSON":
		if s, ok := raw.(string); ok {
			if !json.Valid([]byte(s)) {
				return nil, strconv.ErrSyntax
			}
			return bqCompactJSON([]byte(s))
		}
		b, err := json.Marshal(raw)
		if err != nil {
			return nil, err
		}
		return bqCompactJSON(b)
	default:
		return text, nil
	}
}

// bqMustJSON is the JSON text of a value built from maps, slices and scalars.
func bqMustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func bqCompactJSON(b []byte) (string, error) {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return "", err
	}
	out, err := json.Marshal(v)
	return string(out), err
}

func bqParseFloat(text string) (float64, error) {
	switch strings.ToLower(text) {
	case "nan":
		return math.NaN(), nil
	case "inf", "+inf", "infinity", "+infinity":
		return math.Inf(1), nil
	case "-inf", "-infinity":
		return math.Inf(-1), nil
	}
	return strconv.ParseFloat(text, 64)
}

func bqTrimFraction(s string) string {
	if !strings.Contains(s, ".") {
		return s
	}
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}

// bqFormatClock writes a time of day with microsecond precision, leaving out a
// zero fraction.
func bqFormatClock(t time.Time) string {
	return t.Format("15:04:05.999999")
}

func bqParseDateTime(text string) (time.Time, error) {
	for _, layout := range []string{"2006-1-2T15:04:05.999999999", "2006-1-2 15:04:05.999999999", "2006-1-2"} {
		if t, err := time.Parse(layout, text); err == nil {
			return t, nil
		}
	}
	return time.Time{}, strconv.ErrSyntax
}

var (
	bqZoneSuffixRE = regexp.MustCompile(`^(.*\d:\d{2}:\d{2}(?:\.\d+)?)\s*(UTC|Z|[+-]\d{1,2}(?::?\d{2})?)$`)
	bqDecimalRE    = regexp.MustCompile(`^[-+]?(\d+\.?\d*|\.\d+)([eE][-+]?\d+)?$`)
	bqDateRE       = regexp.MustCompile(`^\d{4}-\d{1,2}-\d{1,2}$`)
	bqTimeRE       = regexp.MustCompile(`^\d{1,2}:\d{2}:\d{2}(\.\d{1,6})?$`)
	bqTimestampRE  = regexp.MustCompile(`^\d{4}-\d{1,2}-\d{1,2}[ T]\d{1,2}:\d{2}:\d{2}(\.\d{1,9})?\s*(UTC|Z|[+-]\d{1,2}(:?\d{2})?)?$`)
)

// bqParseTimestamp reads a TIMESTAMP: a canonical date and time with an
// optional zone (UTC when absent), or a number of seconds since the epoch.
func bqParseTimestamp(raw any, text string) (time.Time, error) {
	switch v := raw.(type) {
	case time.Time:
		return v.UTC(), nil
	case float64:
		return bqEpochSeconds(v), nil
	case json.Number:
		if f, err := v.Float64(); err == nil {
			return bqEpochSeconds(f), nil
		}
	}
	if f, err := strconv.ParseFloat(text, 64); err == nil {
		return bqEpochSeconds(f), nil
	}
	zone := time.UTC
	body := text
	if m := bqZoneSuffixRE.FindStringSubmatch(text); m != nil {
		body = m[1]
		if z := m[2]; z != "UTC" && z != "Z" {
			offset, err := bqZoneOffset(z)
			if err != nil {
				return time.Time{}, err
			}
			zone = time.FixedZone(z, offset)
		}
	}
	t, err := bqParseDateTime(body)
	if err != nil {
		return time.Time{}, err
	}
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), zone).UTC(), nil
}

func bqZoneOffset(z string) (int, error) {
	sign := 1
	if z[0] == '-' {
		sign = -1
	}
	digits := strings.ReplaceAll(z[1:], ":", "")
	hours, minutes := digits, "0"
	if len(digits) > 2 {
		hours, minutes = digits[:len(digits)-2], digits[len(digits)-2:]
	}
	h, err := strconv.Atoi(hours)
	if err != nil {
		return 0, err
	}
	m, err := strconv.Atoi(minutes)
	if err != nil {
		return 0, err
	}
	return sign * (h*3600 + m*60), nil
}

func bqEpochSeconds(f float64) time.Time {
	return time.UnixMicro(int64(math.Round(f * 1e6))).UTC()
}

// bqCellText is the text an export writes for a stored scalar cell.
func bqCellText(f BQFieldSchema, v any) string {
	if bqType(f) == "TIMESTAMP" {
		if s, ok := v.(string); ok {
			if micros, err := strconv.ParseInt(s, 10, 64); err == nil {
				t := time.UnixMicro(micros).UTC()
				return t.Format("2006-01-02 ") + bqFormatClock(t) + " UTC"
			}
		}
	}
	return bqScalarText(v)
}

// bqCellJSON is the JSON value an export writes for a stored cell. INT64,
// NUMERIC and BIGNUMERIC travel as strings so no reader rounds them through a
// double.
func bqCellJSON(f BQFieldSchema, v any) any {
	if v == nil {
		return nil
	}
	if bqRepeated(f) {
		items, _ := v.([]any)
		elem := f
		elem.Mode = "NULLABLE"
		out := make([]any, 0, len(items))
		for _, item := range items {
			out = append(out, bqCellJSON(elem, item))
		}
		return out
	}
	switch bqType(f) {
	case "STRUCT":
		obj, _ := v.(map[string]any)
		return bqRowJSON(f.Fields, obj)
	case "FLOAT64":
		text := bqScalarText(v)
		if f, err := strconv.ParseFloat(text, 64); err == nil && !math.IsInf(f, 0) && !math.IsNaN(f) {
			return json.Number(text)
		}
		return text
	case "BOOL":
		return bqScalarText(v) == "true"
	case "JSON":
		text := bqScalarText(v)
		if json.Valid([]byte(text)) {
			return json.RawMessage(text)
		}
		return text
	default:
		return bqCellText(f, v)
	}
}

// bqRowJSON is a row as an ordered JSON object, leaving NULL cells out.
func bqRowJSON(fields []BQFieldSchema, row map[string]any) bqOrderedObject {
	out := bqOrderedObject{}
	for _, f := range fields {
		if v := row[f.Name]; v != nil {
			out = append(out, bqMember{f.Name, bqCellJSON(f, v)})
		}
	}
	return out
}

type bqMember struct {
	name  string
	value any
}

// bqOrderedObject marshals its members in schema order.
type bqOrderedObject []bqMember

func (o bqOrderedObject) MarshalJSON() ([]byte, error) {
	var b strings.Builder
	b.WriteByte('{')
	for i, m := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		name, err := json.Marshal(m.name)
		if err != nil {
			return nil, err
		}
		value, err := json.Marshal(m.value)
		if err != nil {
			return nil, err
		}
		b.Write(name)
		b.WriteByte(':')
		b.Write(value)
	}
	b.WriteByte('}')
	return []byte(b.String()), nil
}

var bqCoordinateRE = regexp.MustCompile(`-?\d+(\.\d+)?([eE][-+]?\d+)?`)

// bqCellSize is a cell's logical size, the measure BigQuery bills storage and
// reports in numBytes by: a fixed width per type, the length of a STRING or
// BYTES value plus two, the sum of a RECORD's or an ARRAY's members, and zero
// for NULL.
func bqCellSize(f BQFieldSchema, v any) int64 {
	if v == nil {
		return 0
	}
	if bqRepeated(f) {
		items, _ := v.([]any)
		elem := f
		elem.Mode = "NULLABLE"
		var size int64
		for _, item := range items {
			size += bqCellSize(elem, item)
		}
		return size
	}
	switch bqType(f) {
	case "STRUCT":
		obj, _ := v.(map[string]any)
		return bqRowSize(f.Fields, obj)
	case "INT64", "FLOAT64", "DATE", "DATETIME", "TIME", "TIMESTAMP":
		return 8
	case "BOOL":
		return 1
	case "NUMERIC", "INTERVAL", "RANGE":
		return 16
	case "BIGNUMERIC":
		return 32
	case "BYTES":
		text := bqScalarText(v)
		if b, err := base64.StdEncoding.DecodeString(text); err == nil {
			return 2 + int64(len(b))
		}
		return 2 + int64(len(text))
	case "GEOGRAPHY":
		return 16 + 24*int64(len(bqCoordinateRE.FindAllString(bqScalarText(v), -1))/2)
	case "JSON":
		return int64(len(bqScalarText(v)))
	default:
		return 2 + int64(len(bqScalarText(v)))
	}
}

func bqRowSize(fields []BQFieldSchema, row map[string]any) int64 {
	var size int64
	for _, f := range fields {
		size += bqCellSize(f, row[f.Name])
	}
	return size
}

// bqRowsSize is the logical size of rows under schema. A table written without
// a schema sizes each cell as the STRING of its text.
func bqRowsSize(schema *BQSchema, rows []map[string]any) int64 {
	var size int64
	for _, row := range rows {
		if schema != nil && len(schema.Fields) > 0 {
			size += bqRowSize(schema.Fields, row)
			continue
		}
		for _, v := range row {
			if v != nil {
				size += 2 + int64(len(bqScalarText(v)))
			}
		}
	}
	return size
}
