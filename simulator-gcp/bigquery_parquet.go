package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/format"
)

// bqParquetOptions is JobConfigurationLoad.parquetOptions.
type bqParquetOptions struct {
	EnableListInference bool `json:"enableListInference"`
	EnumAsString        bool `json:"enumAsString"`
}

func bqParquetLogical(n parquet.Node) format.LogicalTypeValue {
	if lt := n.Type().LogicalType(); lt != nil {
		return lt.Value
	}
	return nil
}

func bqParquetMode(n parquet.Node) string {
	switch {
	case n.Repeated():
		return "REPEATED"
	case n.Optional():
		return "NULLABLE"
	default:
		return "REQUIRED"
	}
}

// bqParquetUnitDuration is the length of one tick of a TIME or TIMESTAMP
// column.
func bqParquetUnitDuration(u format.TimeUnit) time.Duration {
	switch u.Value.(type) {
	case *format.MilliSeconds:
		return time.Millisecond
	case *format.NanoSeconds:
		return time.Nanosecond
	default:
		return time.Microsecond
	}
}

// bqParquetField is the column a Parquet field loads as. A LIST loads as a
// RECORD holding a repeated "list" RECORD of "element" values, or, under
// enableListInference, as a REPEATED column of its elements.
func bqParquetField(name string, n parquet.Node, opts bqParquetOptions) (BQFieldSchema, error) {
	f := BQFieldSchema{Name: name, Mode: bqParquetMode(n)}
	if !n.Leaf() {
		switch bqParquetLogical(n).(type) {
		case *format.ListType:
			inner := n.Fields()
			if len(inner) != 1 || len(inner[0].Fields()) != 1 {
				return f, bqFail("invalid", "Unsupported Parquet LIST layout. Field: %s", name)
			}
			elemNode := inner[0].Fields()[0]
			elem, err := bqParquetField("element", elemNode, opts)
			if err != nil {
				return f, err
			}
			if opts.EnableListInference {
				if elem.Mode == "REPEATED" {
					return f, bqFail("invalid", "Unsupported nested Parquet LIST. Field: %s", name)
				}
				elem.Name, elem.Mode = name, "REPEATED"
				return elem, nil
			}
			f.Type = "RECORD"
			f.Fields = []BQFieldSchema{{Name: "list", Type: "RECORD", Mode: "REPEATED", Fields: []BQFieldSchema{elem}}}
			return f, nil
		case *format.MapType:
			return f, bqFail("invalid", "the simulator does not load Parquet MAP columns. Field: %s", name)
		}
		f.Type = "RECORD"
		for _, sub := range n.Fields() {
			field, err := bqParquetField(sub.Name(), sub, opts)
			if err != nil {
				return f, err
			}
			f.Fields = append(f.Fields, field)
		}
		return f, nil
	}
	switch lt := bqParquetLogical(n).(type) {
	case *format.StringType, *format.UUIDType:
		f.Type = "STRING"
		return f, nil
	case *format.EnumType:
		f.Type = "BYTES"
		if opts.EnumAsString {
			f.Type = "STRING"
		}
		return f, nil
	case *format.JsonType:
		f.Type = "JSON"
		return f, nil
	case *format.DateType:
		f.Type = "DATE"
		return f, nil
	case *format.TimeType:
		f.Type = "TIME"
		return f, nil
	case *format.TimestampType:
		f.Type = "TIMESTAMP"
		if !lt.IsAdjustedToUTC {
			f.Type = "DATETIME"
		}
		return f, nil
	case *format.DecimalType:
		f.Type = "NUMERIC"
		if lt.Scale > 9 || lt.Precision-lt.Scale > 29 {
			f.Type = "BIGNUMERIC"
		}
		return f, nil
	}
	switch n.Type().Kind() {
	case parquet.Boolean:
		f.Type = "BOOLEAN"
	case parquet.Int32, parquet.Int64:
		f.Type = "INTEGER"
	case parquet.Int96:
		f.Type = "TIMESTAMP"
	case parquet.Float, parquet.Double:
		f.Type = "FLOAT"
	default:
		f.Type = "BYTES"
	}
	return f, nil
}

// bqParquetValue turns a reconstructed Parquet value into the source value
// its column converts from.
func bqParquetValue(n parquet.Node, v any, opts bqParquetOptions) (any, error) {
	if v == nil {
		return nil, nil
	}
	if n.Repeated() {
		items, _ := v.([]any)
		out := make([]any, 0, len(items))
		for _, item := range items {
			converted, err := bqParquetValue(parquet.Required(n), item, opts)
			if err != nil {
				return nil, err
			}
			out = append(out, converted)
		}
		return out, nil
	}
	if !n.Leaf() {
		if _, isList := bqParquetLogical(n).(*format.ListType); isList {
			elemNode := n.Fields()[0].Fields()[0]
			items, _ := v.([]any)
			out := make([]any, 0, len(items))
			for _, item := range items {
				converted, err := bqParquetValue(elemNode, item, opts)
				if err != nil {
					return nil, err
				}
				if opts.EnableListInference {
					out = append(out, converted)
				} else {
					out = append(out, map[string]any{"element": converted})
				}
			}
			if opts.EnableListInference {
				return out, nil
			}
			return map[string]any{"list": out}, nil
		}
		obj, _ := v.(map[string]any)
		out := make(map[string]any, len(obj))
		for _, sub := range n.Fields() {
			converted, err := bqParquetValue(sub, obj[sub.Name()], opts)
			if err != nil {
				return nil, err
			}
			out[sub.Name()] = converted
		}
		return out, nil
	}
	switch lt := bqParquetLogical(n).(type) {
	case *format.JsonType:
		if s, ok := v.(string); ok {
			return s, nil
		}
		b, err := json.Marshal(v)
		return string(b), err
	case *format.DateType:
		days, err := bqParquetInt(v)
		return time.Unix(days*86400, 0).UTC(), err
	case *format.TimeType:
		ticks, err := bqParquetInt(v)
		return time.Time{}.Add(time.Duration(ticks) * bqParquetUnitDuration(lt.Unit)), err
	case *format.TimestampType:
		ticks, err := bqParquetInt(v)
		return time.Unix(0, 0).Add(time.Duration(ticks) * bqParquetUnitDuration(lt.Unit)).UTC(), err
	case *format.DecimalType:
		switch d := v.(type) {
		case *big.Float:
			return d.Text('f', int(lt.Scale)), nil
		case *big.Rat:
			return d.FloatString(int(lt.Scale)), nil
		}
		unscaled, err := bqParquetInt(v)
		if err != nil {
			return nil, err
		}
		return new(big.Rat).SetFrac(big.NewInt(unscaled), new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(lt.Scale)), nil)).FloatString(int(lt.Scale)), nil
	case *format.StringType, *format.UUIDType:
		if b, ok := v.([]byte); ok {
			return string(b), nil
		}
		return v, nil
	case *format.EnumType:
		if opts.EnumAsString {
			return fmt.Sprint(v), nil
		}
	}
	switch n.Type().Kind() {
	case parquet.ByteArray, parquet.FixedLenByteArray:
		switch b := v.(type) {
		case string:
			return []byte(b), nil
		case []byte:
			return b, nil
		}
		if s, ok := v.(fmt.Stringer); ok {
			return []byte(s.String()), nil
		}
	case parquet.Int32, parquet.Int64:
		return bqParquetInt(v)
	case parquet.Float:
		if f, ok := v.(float32); ok {
			return float64(f), nil
		}
	}
	return v, nil
}

func bqParquetInt(v any) (int64, error) {
	switch n := v.(type) {
	case int32:
		return int64(n), nil
	case int64:
		return n, nil
	case int:
		return int64(n), nil
	case uint32:
		return int64(n), nil
	case uint64:
		return int64(n), nil
	}
	return 0, fmt.Errorf("unexpected Parquet integer %T", v)
}

// bqParseParquetSources reads Parquet files. Without a known schema the
// first file's schema becomes the table's.
func bqParseParquetSources(cfg bqLoadConfig, schema *BQSchema, sources []bqSource) ([]map[string]any, *BQSchema, error) {
	var opts bqParquetOptions
	if cfg.ParquetOptions != nil {
		opts = *cfg.ParquetOptions
	}
	var (
		rows       []map[string]any
		fileSchema *BQSchema
	)
	for _, src := range sources {
		file, err := parquet.OpenFile(bytes.NewReader(src.data), int64(len(src.data)))
		if err != nil {
			return nil, nil, bqFail("invalid", "Error while reading data, error message: %s is not a Parquet file: %v", src.uri, err)
		}
		fields := file.Schema().Fields()
		if fileSchema == nil {
			fileSchema = &BQSchema{}
			for _, f := range fields {
				field, err := bqParquetField(f.Name(), f, opts)
				if err != nil {
					return nil, nil, err
				}
				fileSchema.Fields = append(fileSchema.Fields, field)
			}
		}
		reader := parquet.NewReader(file)
		for {
			value := map[string]any{}
			if err := reader.Read(&value); err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				return nil, nil, bqFail("invalid", "Error while reading data, error message: %v; file: %s", err, src.uri)
			}
			row := make(map[string]any, len(fields))
			for _, f := range fields {
				converted, err := bqParquetValue(f, value[f.Name()], opts)
				if err != nil {
					return nil, nil, bqFail("invalid", "Error while reading data, error message: %v; file: %s", err, src.uri)
				}
				row[f.Name()] = converted
			}
			rows = append(rows, row)
		}
		if err := reader.Close(); err != nil {
			return nil, nil, err
		}
	}
	return bqConformRows(schema, fileSchema, rows)
}

// bqParquetGroup is a Parquet group whose columns keep the table's order;
// parquet.Group alone orders them by name.
type bqParquetGroup struct {
	parquet.Group
	rank map[string]int
}

func (g bqParquetGroup) Fields() []parquet.Field {
	fields := g.Group.Fields()
	sort.SliceStable(fields, func(i, j int) bool { return g.rank[fields[i].Name()] < g.rank[fields[j].Name()] })
	return fields
}

func bqParquetGroupOf(fields []BQFieldSchema) parquet.Node {
	g := bqParquetGroup{Group: parquet.Group{}, rank: map[string]int{}}
	for i, f := range fields {
		g.Group[f.Name] = bqParquetNode(f)
		g.rank[f.Name] = i
	}
	return g
}

// bqParquetNode is the Parquet column an extract writes for f.
func bqParquetNode(f BQFieldSchema) parquet.Node {
	var n parquet.Node
	switch bqType(f) {
	case "STRUCT":
		n = bqParquetGroupOf(f.Fields)
	case "INT64":
		n = parquet.Int(64)
	case "FLOAT64":
		n = parquet.Leaf(parquet.DoubleType)
	case "BOOL":
		n = parquet.Leaf(parquet.BooleanType)
	case "BYTES":
		n = parquet.Leaf(parquet.ByteArrayType)
	case "NUMERIC":
		n = parquet.Decimal(9, 38, parquet.FixedLenByteArrayType(16))
	case "BIGNUMERIC":
		n = parquet.Decimal(38, 76, parquet.FixedLenByteArrayType(32))
	case "DATE":
		n = parquet.Date()
	case "TIME":
		n = parquet.Time(parquet.Microsecond)
	case "TIMESTAMP":
		n = parquet.Timestamp(parquet.Microsecond)
	case "DATETIME":
		n = parquet.TimestampAdjusted(parquet.Microsecond, false)
	case "JSON":
		n = parquet.JSON()
	default:
		n = parquet.String()
	}
	switch {
	case bqRepeated(f):
		return parquet.Repeated(n)
	case strings.EqualFold(f.Mode, "REQUIRED"):
		return n
	default:
		return parquet.Optional(n)
	}
}

// bqEncodeParquet writes rows as a Parquet file.
func bqEncodeParquet(fields []BQFieldSchema, rows []map[string]any, compression string) ([]byte, error) {
	schema := parquet.NewSchema("Root", bqParquetGroupOf(fields))
	options := []parquet.WriterOption{schema}
	switch compression {
	case "GZIP":
		options = append(options, parquet.Compression(&parquet.Gzip))
	case "SNAPPY":
		options = append(options, parquet.Compression(&parquet.Snappy))
	case "ZSTD":
		options = append(options, parquet.Compression(&parquet.Zstd))
	}
	values := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		value, err := bqParquetRecord(fields, row)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	var buf bytes.Buffer
	w := parquet.NewGenericWriter[map[string]any](&buf, options...)
	if _, err := w.Write(values); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func bqParquetRecord(fields []BQFieldSchema, row map[string]any) (map[string]any, error) {
	out := make(map[string]any, len(fields))
	for _, f := range fields {
		v, err := bqParquetCell(f, row[f.Name])
		if err != nil {
			return nil, err
		}
		out[f.Name] = v
	}
	return out, nil
}

// bqParquetCell converts a stored cell to the Go value its Parquet column
// encodes.
func bqParquetCell(f BQFieldSchema, v any) (any, error) {
	if bqRepeated(f) {
		items, _ := v.([]any)
		elem := f
		elem.Mode = "REQUIRED"
		out := make([]any, 0, len(items))
		for _, item := range items {
			converted, err := bqParquetCell(elem, item)
			if err != nil {
				return nil, err
			}
			out = append(out, converted)
		}
		return out, nil
	}
	if v == nil {
		return nil, nil
	}
	if bqType(f) == "STRUCT" {
		obj, _ := v.(map[string]any)
		return bqParquetRecord(f.Fields, obj)
	}
	text := bqScalarText(v)
	switch bqType(f) {
	case "INT64":
		return strconv.ParseInt(text, 10, 64)
	case "FLOAT64":
		return bqParseFloat(text)
	case "BOOL":
		return text == "true", nil
	case "BYTES":
		return base64.StdEncoding.DecodeString(text)
	case "NUMERIC", "BIGNUMERIC":
		scale, size := 9, 16
		if bqType(f) == "BIGNUMERIC" {
			scale, size = 38, 32
		}
		r, ok := new(big.Rat).SetString(text)
		if !ok {
			return nil, fmt.Errorf("field %s holds %q, not a %s", f.Name, text, bqType(f))
		}
		unscaled := new(big.Int).Quo(new(big.Int).Mul(r.Num(), new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale)), nil)), r.Denom())
		return bqTwosComplement(unscaled, size), nil
	case "DATE":
		t, err := time.Parse("2006-01-02", text)
		return int32(t.Unix() / 86400), err
	case "TIME":
		t, err := time.Parse("15:04:05.999999", text)
		return int64(t.Sub(time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC)) / time.Microsecond), err
	case "TIMESTAMP":
		if micros, err := strconv.ParseInt(text, 10, 64); err == nil {
			return micros, nil
		}
		t, err := bqParseTimestamp(text, text)
		return t.UnixMicro(), err
	case "DATETIME":
		t, err := bqParseDateTime(text)
		return t.UnixMicro(), err
	default:
		return bqCellText(f, v), nil
	}
}
