package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"math/big"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hamba/avro/v2"
	"github.com/hamba/avro/v2/ocf"
)

// bqAvroDeref follows a reference to the schema it names.
func bqAvroDeref(s avro.Schema) avro.Schema {
	if ref, ok := s.(*avro.RefSchema); ok {
		return ref.Schema()
	}
	return s
}

func bqAvroLogical(s avro.Schema) avro.LogicalSchema {
	if lts, ok := s.(avro.LogicalTypeSchema); ok {
		return lts.Logical()
	}
	return nil
}

// bqAvroTypeName is the name a union member goes by when a union value is
// read or written: the full name of a named type, otherwise the type with
// its logical type.
func bqAvroTypeName(s avro.Schema) string {
	s = bqAvroDeref(s)
	if named, ok := s.(avro.NamedSchema); ok {
		return named.FullName()
	}
	name := string(s.Type())
	if ls := bqAvroLogical(s); ls != nil {
		name += "." + string(ls.Type())
	}
	return name
}

// bqAvroBranch is the one non-null member of a union BigQuery reads, and
// whether the union admits null.
func bqAvroBranch(name string, u *avro.UnionSchema) (avro.Schema, bool, error) {
	var branch avro.Schema
	nullable := false
	for _, t := range u.Types() {
		if t.Type() == avro.Null {
			nullable = true
			continue
		}
		if branch != nil {
			return nil, false, bqFail("invalid", "Unsupported Avro union of several non-null types. Field: %s", name)
		}
		branch = t
	}
	if branch == nil {
		return nil, false, bqFail("invalid", "Unsupported Avro union of only null. Field: %s", name)
	}
	return branch, nullable, nil
}

// bqAvroField is the column an Avro field loads as.
func bqAvroField(name string, s avro.Schema, logical bool) (BQFieldSchema, error) {
	s = bqAvroDeref(s)
	mode := "REQUIRED"
	if u, ok := s.(*avro.UnionSchema); ok {
		branch, nullable, err := bqAvroBranch(name, u)
		if err != nil {
			return BQFieldSchema{}, err
		}
		s = bqAvroDeref(branch)
		if nullable {
			mode = "NULLABLE"
		}
	}
	f := BQFieldSchema{Name: name, Mode: mode}
	switch t := s.(type) {
	case *avro.ArraySchema:
		elem, err := bqAvroField(name, t.Items(), logical)
		if err != nil {
			return f, err
		}
		elem.Mode = "REPEATED"
		return elem, nil
	case *avro.MapSchema:
		value, err := bqAvroField("value", t.Values(), logical)
		if err != nil {
			return f, err
		}
		f.Type, f.Mode = "RECORD", "REPEATED"
		f.Fields = []BQFieldSchema{{Name: "key", Type: "STRING", Mode: "REQUIRED"}, value}
		return f, nil
	case *avro.RecordSchema:
		f.Type = "RECORD"
		for _, sub := range t.Fields() {
			field, err := bqAvroField(sub.Name(), sub.Type(), logical)
			if err != nil {
				return f, err
			}
			f.Fields = append(f.Fields, field)
		}
		return f, nil
	case *avro.EnumSchema:
		f.Type = "STRING"
		return f, nil
	case *avro.FixedSchema:
		f.Type = "BYTES"
		if ls := t.Logical(); logical && ls != nil && ls.Type() == avro.Decimal {
			f.Type = bqAvroDecimalType(ls)
		}
		return f, nil
	}
	ls := bqAvroLogical(s)
	if logical && ls != nil {
		switch ls.Type() {
		case avro.Date:
			f.Type = "DATE"
			return f, nil
		case avro.TimeMillis, avro.TimeMicros:
			f.Type = "TIME"
			return f, nil
		case avro.TimestampMillis, avro.TimestampMicros:
			f.Type = "TIMESTAMP"
			return f, nil
		case avro.LocalTimestampMillis, avro.LocalTimestampMicros:
			f.Type = "DATETIME"
			return f, nil
		case avro.Decimal:
			f.Type = bqAvroDecimalType(ls)
			return f, nil
		}
	}
	switch s.Type() {
	case avro.Boolean:
		f.Type = "BOOLEAN"
	case avro.Int, avro.Long:
		f.Type = "INTEGER"
	case avro.Float, avro.Double:
		f.Type = "FLOAT"
	case avro.Bytes:
		f.Type = "BYTES"
	case avro.String:
		f.Type = "STRING"
	default:
		return f, bqFail("invalid", "Unsupported Avro type %s. Field: %s", s.Type(), name)
	}
	return f, nil
}

// bqAvroDecimalType is NUMERIC when the decimal fits it, BIGNUMERIC otherwise.
func bqAvroDecimalType(ls avro.LogicalSchema) string {
	if d, ok := ls.(*avro.DecimalLogicalSchema); ok && (d.Scale() > 9 || d.Precision()-d.Scale() > 29) {
		return "BIGNUMERIC"
	}
	return "NUMERIC"
}

// bqAvroValue turns a decoded Avro value into the source value its column
// converts from.
func bqAvroValue(s avro.Schema, v any, logical bool) any {
	if v == nil {
		return nil
	}
	s = bqAvroDeref(s)
	switch t := s.(type) {
	case *avro.UnionSchema:
		members, ok := v.(map[string]any)
		if !ok {
			return v
		}
		for _, branch := range t.Types() {
			if item, ok := members[bqAvroTypeName(branch)]; ok {
				return bqAvroValue(branch, item, logical)
			}
		}
		return nil
	case *avro.RecordSchema:
		obj, _ := v.(map[string]any)
		out := make(map[string]any, len(obj))
		for _, f := range t.Fields() {
			out[f.Name()] = bqAvroValue(f.Type(), obj[f.Name()], logical)
		}
		return out
	case *avro.ArraySchema:
		items, _ := v.([]any)
		out := make([]any, len(items))
		for i, item := range items {
			out[i] = bqAvroValue(t.Items(), item, logical)
		}
		return out
	case *avro.MapSchema:
		obj, _ := v.(map[string]any)
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make([]any, 0, len(obj))
		for _, k := range keys {
			out = append(out, map[string]any{"key": k, "value": bqAvroValue(t.Values(), obj[k], logical)})
		}
		return out
	}
	ls := bqAvroLogical(s)
	switch val := v.(type) {
	case time.Time:
		switch {
		case ls != nil && ls.Type() == avro.Date:
			if logical {
				return val
			}
			return val.Unix() / 86400
		case ls != nil && (ls.Type() == avro.TimestampMillis || ls.Type() == avro.LocalTimestampMillis):
			if logical {
				return val
			}
			return val.UnixMilli()
		default:
			if logical {
				return val
			}
			return val.UnixMicro()
		}
	case time.Duration:
		if logical {
			return time.Time{}.Add(val)
		}
		if ls != nil && ls.Type() == avro.TimeMillis {
			return val.Milliseconds()
		}
		return val.Microseconds()
	case *big.Rat:
		d, _ := ls.(*avro.DecimalLogicalSchema)
		scale := 0
		if d != nil {
			scale = d.Scale()
		}
		if logical {
			return val.FloatString(scale)
		}
		unscaled := new(big.Int).Quo(new(big.Int).Mul(val.Num(), new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale)), nil)), val.Denom())
		return bqTwosComplement(unscaled, 0)
	case int:
		return int64(val)
	case int32:
		return int64(val)
	case float32:
		return float64(val)
	}
	if rv := reflect.ValueOf(v); rv.Kind() == reflect.Array && rv.Type().Elem().Kind() == reflect.Uint8 {
		out := make([]byte, rv.Len())
		reflect.Copy(reflect.ValueOf(out), rv)
		return out
	}
	return v
}

// bqTwosComplement is n as big-endian two's complement, at least size bytes.
func bqTwosComplement(n *big.Int, size int) []byte {
	byteLen := (n.BitLen() + 8) / 8
	if byteLen < size {
		byteLen = size
	}
	if n.Sign() >= 0 {
		return n.FillBytes(make([]byte, byteLen))
	}
	mod := new(big.Int).Lsh(big.NewInt(1), uint(byteLen*8))
	return new(big.Int).Add(mod, n).FillBytes(make([]byte, byteLen))
}

// bqParseAvroSources reads Avro object container files. Without a known
// schema the first file's schema becomes the table's.
func bqParseAvroSources(cfg bqLoadConfig, schema *BQSchema, sources []bqSource) ([]map[string]any, *BQSchema, error) {
	var (
		rows       []map[string]any
		fileSchema *BQSchema
	)
	for _, src := range sources {
		dec, err := ocf.NewDecoder(bytes.NewReader(src.data))
		if err != nil {
			return nil, nil, bqFail("invalid", "Error while reading data, error message: The Apache Avro library failed to parse the header: %v; file: %s", err, src.uri)
		}
		record, ok := bqAvroDeref(dec.Schema()).(*avro.RecordSchema)
		if !ok {
			return nil, nil, bqFail("invalid", "Error while reading data, error message: the Avro schema of %s is not a record", src.uri)
		}
		if fileSchema == nil {
			fileSchema = &BQSchema{}
			for _, f := range record.Fields() {
				field, err := bqAvroField(f.Name(), f.Type(), cfg.UseAvroLogicalTypes)
				if err != nil {
					return nil, nil, err
				}
				fileSchema.Fields = append(fileSchema.Fields, field)
			}
		}
		for dec.HasNext() {
			var value map[string]any
			if err := dec.Decode(&value); err != nil {
				return nil, nil, bqFail("invalid", "Error while reading data, error message: %v; file: %s", err, src.uri)
			}
			row, ok := bqAvroValue(record, value, cfg.UseAvroLogicalTypes).(map[string]any)
			if !ok {
				return nil, nil, bqFail("invalid", "Error while reading data, error message: a record of %s is not an Avro record", src.uri)
			}
			rows = append(rows, row)
		}
		if err := dec.Error(); err != nil {
			return nil, nil, bqFail("invalid", "Error while reading data, error message: %v; file: %s", err, src.uri)
		}
	}
	return bqConformRows(schema, fileSchema, rows)
}

// bqConformRows converts the rows of a self-describing source under the
// table's schema, matching columns by name, or under the file's own schema
// when the table has none.
func bqConformRows(schema, fileSchema *BQSchema, rows []map[string]any) ([]map[string]any, *BQSchema, error) {
	if schema == nil {
		if fileSchema == nil {
			return nil, nil, bqFail("invalid", "No schema specified on job or table.")
		}
		schema = fileSchema
	} else if fileSchema != nil {
		if err := bqSchemaByName(schema, fileSchema.Fields); err != nil {
			return nil, nil, err
		}
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		folded := make(map[string]any, len(row))
		for _, f := range schema.Fields {
			for k, v := range row {
				if strings.EqualFold(k, f.Name) {
					folded[f.Name] = v
				}
			}
		}
		converted, err := bqCoerceRecord("", schema.Fields, folded, true)
		if err != nil {
			return nil, nil, bqFail("invalid", "Error while reading data, error message: %v", err)
		}
		out = append(out, converted)
	}
	return out, schema, nil
}

// bqAvroWriter builds the Avro schema an extract writes and converts each
// stored cell to the value that schema encodes.
type bqAvroWriter struct {
	logical bool
	names   map[string]int
}

func (w *bqAvroWriter) recordName(base string) string {
	name := bqColumnName(base)
	if name == "" {
		name = "Record"
	}
	w.names[name]++
	if n := w.names[name]; n > 1 {
		name += "_" + strconv.Itoa(n)
	}
	return name
}

// scalarType is the Avro type of a scalar column.
func (w *bqAvroWriter) scalarType(f BQFieldSchema) any {
	switch bqType(f) {
	case "INT64":
		return "long"
	case "FLOAT64":
		return "double"
	case "BOOL":
		return "boolean"
	case "BYTES":
		return "bytes"
	case "NUMERIC":
		return map[string]any{"type": "bytes", "logicalType": "decimal", "precision": 38, "scale": 9}
	case "BIGNUMERIC":
		return map[string]any{"type": "bytes", "logicalType": "decimal", "precision": 77, "scale": 38}
	case "TIMESTAMP":
		return map[string]any{"type": "long", "logicalType": "timestamp-micros"}
	case "DATE":
		if w.logical {
			return map[string]any{"type": "int", "logicalType": "date"}
		}
	case "TIME":
		if w.logical {
			return map[string]any{"type": "long", "logicalType": "time-micros"}
		}
	case "DATETIME":
		if w.logical {
			return map[string]any{"type": "long", "logicalType": "local-timestamp-micros"}
		}
	}
	return "string"
}

// fieldType is the Avro type of a column, unwrapped from its NULLABLE union.
func (w *bqAvroWriter) fieldType(f BQFieldSchema) any {
	if bqType(f) == "STRUCT" {
		fields := make([]any, 0, len(f.Fields))
		for _, sub := range f.Fields {
			fields = append(fields, w.field(sub))
		}
		return map[string]any{"type": "record", "name": w.recordName(f.Name), "fields": fields}
	}
	return w.scalarType(f)
}

func (w *bqAvroWriter) field(f BQFieldSchema) map[string]any {
	t := w.fieldType(f)
	switch {
	case bqRepeated(f):
		return map[string]any{"name": f.Name, "type": map[string]any{"type": "array", "items": t}}
	case strings.EqualFold(f.Mode, "REQUIRED"):
		return map[string]any{"name": f.Name, "type": t}
	default:
		return map[string]any{"name": f.Name, "type": []any{"null", t}, "default": nil}
	}
}

// bqEncodeAvro writes rows as an Avro object container file.
func bqEncodeAvro(fields []BQFieldSchema, rows []map[string]any, compression string, logical bool) ([]byte, error) {
	w := &bqAvroWriter{logical: logical, names: map[string]int{}}
	columns := make([]any, 0, len(fields))
	for _, f := range fields {
		columns = append(columns, w.field(f))
	}
	schema, err := avro.Parse(bqMustJSON(map[string]any{"type": "record", "name": "Root", "fields": columns}))
	if err != nil {
		return nil, err
	}
	codec := ocf.Null
	switch compression {
	case "DEFLATE":
		codec = ocf.Deflate
	case "SNAPPY":
		codec = ocf.Snappy
	}
	var buf bytes.Buffer
	enc, err := ocf.NewEncoderWithSchema(schema, &buf, ocf.WithCodec(codec))
	if err != nil {
		return nil, err
	}
	record, ok := bqAvroDeref(schema).(*avro.RecordSchema)
	if !ok {
		return nil, fmt.Errorf("the extract schema is not an Avro record")
	}
	for _, row := range rows {
		value := make(map[string]any, len(record.Fields()))
		for i, f := range record.Fields() {
			v, err := bqAvroEncodeValue(fields[i], f.Type(), row[fields[i].Name])
			if err != nil {
				return nil, err
			}
			value[f.Name()] = v
		}
		if err := enc.Encode(value); err != nil {
			return nil, err
		}
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// bqAvroEncodeValue converts a stored cell to the value its Avro type takes.
// A union value names its branch.
func bqAvroEncodeValue(f BQFieldSchema, s avro.Schema, v any) (any, error) {
	s = bqAvroDeref(s)
	if u, ok := s.(*avro.UnionSchema); ok {
		if v == nil {
			return nil, nil
		}
		branch := u.Types()[1]
		inner, err := bqAvroEncodeValue(f, branch, v)
		if err != nil {
			return nil, err
		}
		return map[string]any{bqAvroTypeName(branch): inner}, nil
	}
	if arr, ok := s.(*avro.ArraySchema); ok {
		items, _ := v.([]any)
		elem := f
		elem.Mode = "REQUIRED"
		out := make([]any, 0, len(items))
		for _, item := range items {
			converted, err := bqAvroEncodeValue(elem, arr.Items(), item)
			if err != nil {
				return nil, err
			}
			out = append(out, converted)
		}
		return out, nil
	}
	if rec, ok := s.(*avro.RecordSchema); ok {
		obj, _ := v.(map[string]any)
		out := make(map[string]any, len(rec.Fields()))
		for i, sub := range rec.Fields() {
			converted, err := bqAvroEncodeValue(f.Fields[i], sub.Type(), obj[f.Fields[i].Name])
			if err != nil {
				return nil, err
			}
			out[sub.Name()] = converted
		}
		return out, nil
	}
	if v == nil {
		return nil, fmt.Errorf("REQUIRED field %s holds NULL", f.Name)
	}
	text := bqScalarText(v)
	ls := bqAvroLogical(s)
	switch {
	case ls != nil && ls.Type() == avro.Decimal:
		r, ok := new(big.Rat).SetString(text)
		if !ok {
			return nil, fmt.Errorf("field %s holds %q, not a %s", f.Name, text, bqType(f))
		}
		return r, nil
	case ls != nil && ls.Type() == avro.TimestampMicros:
		micros, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			t, perr := bqParseTimestamp(text, text)
			if perr != nil {
				return nil, perr
			}
			return t, nil
		}
		return time.UnixMicro(micros).UTC(), nil
	case ls != nil && ls.Type() == avro.Date:
		return time.Parse("2006-01-02", text)
	case ls != nil && ls.Type() == avro.TimeMicros:
		t, err := time.Parse("15:04:05.999999", text)
		if err != nil {
			return nil, err
		}
		return t.Sub(time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC)), nil
	case ls != nil && ls.Type() == avro.LocalTimestampMicros:
		return bqParseDateTime(text)
	}
	switch s.Type() {
	case avro.Long:
		return strconv.ParseInt(text, 10, 64)
	case avro.Double:
		return bqParseFloat(text)
	case avro.Boolean:
		return text == "true", nil
	case avro.Bytes:
		return base64.StdEncoding.DecodeString(text)
	default:
		return bqCellText(f, v), nil
	}
}
