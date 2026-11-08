package main

import (
	"bytes"
	"math"
	"math/rand/v2"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// The pipeline functions, each as the vendored firestore-pipeline-*-functions
// page defines it. A function whose argument is NULL answers NULL unless its
// page says otherwise (concat's, length's and reverse's examples), and one
// whose argument has a type it does not take is an error.

func fpArgs(env *fpEnv, row fpRow, fn *fsFunction) ([]pval, error) {
	out := make([]pval, len(fn.Args))
	for i, a := range fn.Args {
		v, err := fpEval(env, row, a)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

func fpNullish(v pval) bool { return v.absent || v.v.NullValue != nil }

func fpWantArgs(fn *fsFunction, min, max int) error {
	if len(fn.Args) < min || (max >= 0 && len(fn.Args) > max) {
		if min == max {
			return fpErrorf("%s(...) takes %d arguments, got %d", fn.Name, min, len(fn.Args))
		}
		return fpErrorf("%s(...) takes between %d and %d arguments, got %d", fn.Name, min, max, len(fn.Args))
	}
	return nil
}

func fpTypeError(fn *fsFunction, v pval) error {
	return fpErrorf("%s(...) does not take a %s argument", fn.Name, fpTypeName(v))
}

// fpTypeName is type(...)'s name for a value (type-functions.md.gz).
func fpTypeName(v pval) string {
	if v.absent {
		return "absent"
	}
	switch fpKind(v.v) {
	case fkNull:
		return "null"
	case fkBool:
		return "boolean"
	case fkNumber:
		if fpIsInt(v.v) {
			return "int64"
		}
		return "float64"
	case fkTimestamp:
		return "timestamp"
	case fkString:
		return "string"
	case fkBytes:
		return "bytes"
	case fkReference:
		return "reference"
	case fkGeoPoint:
		return "geo_point"
	case fkArray:
		return "array"
	case fkVector:
		return "vector"
	case fkMap:
		return "map"
	}
	return "expression"
}

func fpBoolOf(v pval) (bool, bool) {
	if v.absent || v.v.BooleanValue == nil {
		return false, false
	}
	return *v.v.BooleanValue, true
}

func fpStrOf(v pval) (string, bool) {
	if v.absent || v.v.StringValue == nil {
		return "", false
	}
	return *v.v.StringValue, true
}

func fpCall(env *fpEnv, row fpRow, fn *fsFunction) (pval, error) {
	if lazy, ok := fpLazyFunctions[fn.Name]; ok {
		return lazy(env, row, fn)
	}
	if strict, ok := fpFunctions[fn.Name]; ok {
		args, err := fpArgs(env, row, fn)
		if err != nil {
			return pval{}, err
		}
		return strict(env, row, fn, args)
	}
	if _, ok := fpAccumulators[fn.Name]; ok {
		return pval{}, fpErrorf("%s(...) is an aggregate function and runs only in aggregate(...)", fn.Name)
	}
	return pval{}, fpErrorf("unknown function %q", fn.Name)
}

type fpLazyFunc func(env *fpEnv, row fpRow, fn *fsFunction) (pval, error)
type fpStrictFunc func(env *fpEnv, row fpRow, fn *fsFunction, args []pval) (pval, error)

var fpLazyFunctions map[string]fpLazyFunc
var fpFunctions map[string]fpStrictFunc

func init() {
	fpLazyFunctions = map[string]fpLazyFunc{
		"and":             fpAnd,
		"or":              fpOr,
		"xor":             fpXor,
		"nor":             fpNor,
		"conditional":     fpConditional,
		"if_null":         fpIfNull,
		"if_absent":       fpIfAbsent,
		"if_error":        fpIfError,
		"is_error":        fpIsError,
		"switch_on":       fpSwitchOn,
		"array_filter":    fpArrayFilter,
		"array_transform": fpArrayTransform,
		"array":           fpArrayFunc,
		"scalar":          fpScalarFunc,
	}
	fpFunctions = map[string]fpStrictFunc{
		"equal":                 fpCmpFunc(func(a, b pval) bool { return fpEqualP(a, b) }),
		"not_equal":             fpCmpFunc(func(a, b pval) bool { return !fpEqualP(a, b) }),
		"greater_than":          fpOrderFunc(func(c int) bool { return c > 0 }),
		"greater_than_or_equal": fpOrderFunc(func(c int) bool { return c >= 0 }),
		"less_than":             fpOrderFunc(func(c int) bool { return c < 0 }),
		"less_than_or_equal":    fpOrderFunc(func(c int) bool { return c <= 0 }),
		"cmp":                   fpCmp,
		"not":                   fpNot,
		"equal_any":             fpEqualAny(false),
		"not_equal_any":         fpEqualAny(true),
		"maximum":               fpExtremum(1),
		"minimum":               fpExtremum(-1),
		"exists": func(_ *fpEnv, _ fpRow, fn *fsFunction, a []pval) (pval, error) {
			return fpUnary(fn, a, func(v pval) (pval, error) { return fpBool(!v.absent), nil })
		},
		"is_absent": func(_ *fpEnv, _ fpRow, fn *fsFunction, a []pval) (pval, error) {
			return fpUnary(fn, a, func(v pval) (pval, error) { return fpBool(v.absent), nil })
		},
		"error":            fpErrorFunc,
		"current_document": fpCurrentDocument,
		"current_context":  fpCurrentDocument,
		"type":             fpType,
		"is_type":          fpIsType,
		"add":              fpArith("add"),
		"subtract":         fpArith("subtract"),
		"multiply":         fpArith("multiply"),
		"divide":           fpArith("divide"),
		"mod":              fpArith("mod"),
		"abs":              fpAbs,
		"ceil":             fpRounding(math.Ceil),
		"floor":            fpRounding(math.Floor),
		"round":            fpRoundPlaces(true),
		"trunc":            fpRoundPlaces(false),
		"pow":              fpPow,
		"sqrt": fpMathUnary(func(x float64) (float64, error) {
			if x < 0 && !math.IsNaN(x) {
				return 0, fpErrorf("sqrt(...) of a negative number")
			}
			return math.Sqrt(x), nil
		}),
		"exp": fpMathUnary(func(x float64) (float64, error) { return math.Exp(x), nil }),
		"ln":  fpMathUnary(fpLn),
		"log10": fpMathUnary(func(x float64) (float64, error) {
			if x <= 0 {
				return 0, fpErrorf("log10(...) of a number not above 0")
			}
			return math.Log10(x), nil
		}),
		"log":                       fpLog,
		"rand":                      func(_ *fpEnv, _ fpRow, fn *fsFunction, _ []pval) (pval, error) { return fpDouble(rand.Float64()), nil },
		"concat":                    fpConcat,
		"length":                    fpLength,
		"reverse":                   fpReverse,
		"byte_length":               fpByteLength,
		"char_length":               fpCharLength,
		"starts_with":               fpStrPredicate(strings.HasPrefix),
		"ends_with":                 fpStrPredicate(strings.HasSuffix),
		"string_contains":           fpStrPredicate(strings.Contains),
		"like":                      fpLike,
		"regex_contains":            fpRegex("contains"),
		"regex_find":                fpRegex("find"),
		"regex_find_all":            fpRegex("find_all"),
		"regex_match":               fpRegex("match"),
		"string_concat":             fpStringConcat,
		"string_index_of":           fpStringIndexOf,
		"to_upper":                  fpCase(unicode.ToUpper, bytes.ToUpper),
		"to_lower":                  fpCase(unicode.ToLower, bytes.ToLower),
		"substring":                 fpSubstring,
		"string_reverse":            fpStringReverse,
		"string_repeat":             fpStringRepeat,
		"string_replace_all":        fpStringReplace(-1),
		"string_replace_one":        fpStringReplace(1),
		"trim":                      fpTrim(true, true),
		"ltrim":                     fpTrim(true, false),
		"rtrim":                     fpTrim(false, true),
		"split":                     fpSplit,
		"join":                      fpJoin,
		"array_concat":              fpArrayConcat,
		"array_contains":            fpArrayContains,
		"array_contains_all":        fpArrayContainsSet(true),
		"array_contains_any":        fpArrayContainsSet(false),
		"array_get":                 fpArrayGet,
		"offset":                    fpArrayGet,
		"array_length":              fpArrayLength,
		"array_reverse":             fpArrayReverse,
		"array_first":               fpArrayEnd(true),
		"array_last":                fpArrayEnd(false),
		"array_first_n":             fpArrayEndN(true),
		"array_last_n":              fpArrayEndN(false),
		"array_index_of":            fpArrayIndexOf,
		"array_index_of_all":        fpArrayIndexOfAll,
		"array_slice":               fpArraySlice,
		"maximum_n":                 fpExtremumN(1),
		"minimum_n":                 fpExtremumN(-1),
		"sum":                       fpArraySum,
		"map":                       fpMapFunc,
		"map_get":                   fpMapGet,
		"get_field":                 fpMapGet,
		"map_set":                   fpMapSet,
		"map_remove":                fpMapRemove,
		"map_merge":                 fpMapMerge,
		"map_keys":                  fpMapParts("keys"),
		"map_values":                fpMapParts("values"),
		"map_entries":               fpMapParts("entries"),
		"collection_id":             fpRefPart("collection_id"),
		"document_id":               fpRefPart("document_id"),
		"parent":                    fpRefPart("parent"),
		"reference_slice":           fpReferenceSlice,
		"cosine_distance":           fpVectorFunc("cosine"),
		"euclidean_distance":        fpVectorFunc("euclidean"),
		"manhattan_distance":        fpVectorFunc("manhattan"),
		"dot_product":               fpVectorFunc("dot_product"),
		"vector_length":             fpVectorLength,
		"current_timestamp":         func(env *fpEnv, _ fpRow, _ *fsFunction, _ []pval) (pval, error) { return fpTimestamp(env.now), nil },
		"unix_micros_to_timestamp":  fpUnixToTimestamp(time.Microsecond),
		"unix_millis_to_timestamp":  fpUnixToTimestamp(time.Millisecond),
		"unix_seconds_to_timestamp": fpUnixToTimestamp(time.Second),
		"timestamp_to_unix_micros":  fpTimestampToUnix(time.Microsecond),
		"timestamp_to_unix_millis":  fpTimestampToUnix(time.Millisecond),
		"timestamp_to_unix_seconds": fpTimestampToUnix(time.Second),
		"timestamp_add":             fpTimestampAdd(1),
		"timestamp_sub":             fpTimestampAdd(-1),
		"timestamp_subtract":        fpTimestampAdd(-1),
		"timestamp_diff":            fpTimestampDiff,
		"timestamp_trunc":           fpTimestampTrunc,
		"timestamp_extract":         fpTimestampExtract,
	}
}

func fpUnary(fn *fsFunction, args []pval, f func(pval) (pval, error)) (pval, error) {
	if len(args) != 1 {
		return pval{}, fpErrorf("%s(...) takes 1 argument, got %d", fn.Name, len(args))
	}
	return f(args[0])
}

func fpEqualP(a, b pval) bool {
	if a.absent || b.absent {
		return a.absent && b.absent
	}
	return fpEqual(a.v, b.v)
}

func fpCmpFunc(test func(a, b pval) bool) fpStrictFunc {
	return func(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
		if len(args) != 2 {
			return pval{}, fpErrorf("%s(...) takes 2 arguments, got %d", fn.Name, len(args))
		}
		return fpBool(test(args[0], args[1])), nil
	}
}

func fpOrderFunc(test func(int) bool) fpStrictFunc {
	return func(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
		if len(args) != 2 {
			return pval{}, fpErrorf("%s(...) takes 2 arguments, got %d", fn.Name, len(args))
		}
		if !fpComparable(args[0], args[1]) {
			return fpBool(false), nil
		}
		return fpBool(test(fpCompare(args[0].v, args[1].v))), nil
	}
}

// cmp orders across types as sort does, with ABSENT ordering as NULL.
func fpCmp(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
	if len(args) != 2 {
		return pval{}, fpErrorf("cmp(...) takes 2 arguments, got %d", len(args))
	}
	a, b := args[0], args[1]
	if a.absent {
		a = fpNull()
	}
	if b.absent {
		b = fpNull()
	}
	return fpInt(int64(fpCompare(a.v, b.v))), nil
}

// fpTruth reads a boolean operand: TRUE, FALSE, or unknown for NULL and
// ABSENT; any other value is an error.
func fpTruth(fn *fsFunction, v pval) (val, known bool, err error) {
	if fpNullish(v) {
		return false, false, nil
	}
	b, ok := fpBoolOf(v)
	if !ok {
		return false, false, fpTypeError(fn, v)
	}
	return b, true, nil
}

func fpAnd(env *fpEnv, row fpRow, fn *fsFunction) (pval, error) {
	unknown := false
	for _, a := range fn.Args {
		v, err := fpEval(env, row, a)
		if err != nil {
			return pval{}, err
		}
		b, known, err := fpTruth(fn, v)
		if err != nil {
			return pval{}, err
		}
		if known && !b {
			return fpBool(false), nil
		}
		unknown = unknown || !known
	}
	if unknown {
		return fpNull(), nil
	}
	return fpBool(true), nil
}

func fpOr(env *fpEnv, row fpRow, fn *fsFunction) (pval, error) {
	unknown := false
	for _, a := range fn.Args {
		v, err := fpEval(env, row, a)
		if err != nil {
			return pval{}, err
		}
		b, known, err := fpTruth(fn, v)
		if err != nil {
			return pval{}, err
		}
		if known && b {
			return fpBool(true), nil
		}
		unknown = unknown || !known
	}
	if unknown {
		return fpNull(), nil
	}
	return fpBool(false), nil
}

func fpNor(env *fpEnv, row fpRow, fn *fsFunction) (pval, error) {
	v, err := fpOr(env, row, fn)
	if err != nil || fpNullish(v) {
		return v, err
	}
	return fpBool(!*v.v.BooleanValue), nil
}

func fpXor(env *fpEnv, row fpRow, fn *fsFunction) (pval, error) {
	result, unknown := false, false
	for _, a := range fn.Args {
		v, err := fpEval(env, row, a)
		if err != nil {
			return pval{}, err
		}
		b, known, err := fpTruth(fn, v)
		if err != nil {
			return pval{}, err
		}
		unknown = unknown || !known
		result = result != b
	}
	if unknown {
		return fpNull(), nil
	}
	return fpBool(result), nil
}

func fpNot(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
	return fpUnary(fn, args, func(v pval) (pval, error) {
		b, known, err := fpTruth(fn, v)
		if err != nil || !known {
			return fpNull(), err
		}
		return fpBool(!b), nil
	})
}

func fpConditional(env *fpEnv, row fpRow, fn *fsFunction) (pval, error) {
	if err := fpWantArgs(fn, 3, 3); err != nil {
		return pval{}, err
	}
	c, err := fpEval(env, row, fn.Args[0])
	if err != nil {
		return pval{}, err
	}
	if b, ok := fpBoolOf(c); ok && b {
		return fpEval(env, row, fn.Args[1])
	}
	return fpEval(env, row, fn.Args[2])
}

func fpIfNull(env *fpEnv, row fpRow, fn *fsFunction) (pval, error) {
	if err := fpWantArgs(fn, 2, 2); err != nil {
		return pval{}, err
	}
	v, err := fpEval(env, row, fn.Args[0])
	if err != nil || v.absent || v.v.NullValue == nil {
		return v, err
	}
	return fpEval(env, row, fn.Args[1])
}

func fpIfAbsent(env *fpEnv, row fpRow, fn *fsFunction) (pval, error) {
	if err := fpWantArgs(fn, 2, 2); err != nil {
		return pval{}, err
	}
	v, err := fpEval(env, row, fn.Args[0])
	if err != nil || !v.absent {
		return v, err
	}
	return fpEval(env, row, fn.Args[1])
}

func fpIfError(env *fpEnv, row fpRow, fn *fsFunction) (pval, error) {
	if err := fpWantArgs(fn, 2, 2); err != nil {
		return pval{}, err
	}
	v, err := fpEval(env, row, fn.Args[0])
	if err != nil && fpIsErr(err) {
		return fpEval(env, row, fn.Args[1])
	}
	return v, err
}

func fpIsError(env *fpEnv, row fpRow, fn *fsFunction) (pval, error) {
	if err := fpWantArgs(fn, 1, 1); err != nil {
		return pval{}, err
	}
	_, err := fpEval(env, row, fn.Args[0])
	if err != nil && !fpIsErr(err) {
		return pval{}, err
	}
	return fpBool(err != nil), nil
}

func fpSwitchOn(env *fpEnv, row fpRow, fn *fsFunction) (pval, error) {
	if len(fn.Args) < 2 {
		return pval{}, fpErrorf("switch_on(...) takes at least a condition and its result")
	}
	pairs := len(fn.Args) / 2
	for i := 0; i < pairs; i++ {
		c, err := fpEval(env, row, fn.Args[2*i])
		if err != nil {
			return pval{}, err
		}
		if b, ok := fpBoolOf(c); ok && b {
			return fpEval(env, row, fn.Args[2*i+1])
		}
	}
	if len(fn.Args)%2 == 1 {
		return fpEval(env, row, fn.Args[len(fn.Args)-1])
	}
	return pval{}, fpErrorf("switch_on(...) matched no condition and has no default")
}

func fpErrorFunc(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
	msg := ""
	if len(args) > 0 {
		msg, _ = fpStrOf(args[0])
	}
	return pval{}, fpErrorf("%s", msg)
}

func fpCurrentDocument(_ *fpEnv, row fpRow, _ *fsFunction, _ []pval) (pval, error) {
	fields := map[string]FSValue{}
	for k, v := range row {
		if k == fpLetField || k == fpNameField || k == fpCreateTimeField || k == fpUpdateTimeField {
			continue
		}
		fields[k] = v
	}
	return fpMap(fields), nil
}

func fpEqualAny(negate bool) fpStrictFunc {
	return func(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
		if len(args) != 2 {
			return pval{}, fpErrorf("%s(...) takes 2 arguments, got %d", fn.Name, len(args))
		}
		if args[1].absent || args[1].v.ArrayValue == nil {
			return pval{}, fpTypeError(fn, args[1])
		}
		found := false
		if !args[0].absent {
			for _, e := range args[1].v.ArrayValue.Values {
				if fpEqual(args[0].v, e) {
					found = true
					break
				}
			}
		}
		return fpBool(found != negate), nil
	}
}

// fpExtremum is maximum(...) and minimum(...): over a series of values, or
// over one array, ignoring NULL and ABSENT.
func fpExtremum(sign int) fpStrictFunc {
	return func(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
		values := args
		if len(args) == 1 {
			if fpNullish(args[0]) {
				return fpNull(), nil
			}
			if args[0].v.ArrayValue == nil {
				return pval{}, fpTypeError(fn, args[0])
			}
			values = nil
			for _, e := range args[0].v.ArrayValue.Values {
				values = append(values, fpOf(e))
			}
		}
		var best *FSValue
		for _, v := range values {
			if fpNullish(v) {
				continue
			}
			if best == nil || fpCompare(v.v, *best)*sign > 0 {
				vv := v.v
				best = &vv
			}
		}
		if best == nil {
			return fpNull(), nil
		}
		return fpOf(*best), nil
	}
}

func fpExtremumN(sign int) fpStrictFunc {
	return func(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
		if len(args) != 2 {
			return pval{}, fpErrorf("%s(...) takes 2 arguments, got %d", fn.Name, len(args))
		}
		if fpNullish(args[0]) {
			return fpNull(), nil
		}
		if args[0].v.ArrayValue == nil {
			return pval{}, fpTypeError(fn, args[0])
		}
		n, ok := fpIntOf(args[1].v)
		if !ok || n < 0 {
			return pval{}, fpErrorf("%s(...) takes a non-negative integer count", fn.Name)
		}
		var values []FSValue
		for _, e := range args[0].v.ArrayValue.Values {
			if e.NullValue == nil {
				values = append(values, e)
			}
		}
		sort.SliceStable(values, func(i, j int) bool { return fpCompare(values[i], values[j])*sign > 0 })
		if int64(len(values)) > n {
			values = values[:n]
		}
		return fpArray(values), nil
	}
}

func fpType(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
	return fpUnary(fn, args, func(v pval) (pval, error) {
		if v.absent {
			return fpNull(), nil
		}
		return fpString(fpTypeName(v)), nil
	})
}

// fpTypeNames are the type strings is_type(...) accepts
// (type-functions.md.gz); the Enterprise-only types never match a value the
// simulator stores.
var fpTypeNames = map[string]bool{
	"null": true, "boolean": true, "int32": true, "int64": true, "float64": true, "decimal128": true,
	"number": true, "timestamp": true, "string": true, "bytes": true, "array": true, "map": true,
	"reference": true, "vector": true, "geo_point": true, "max_key": true, "min_key": true,
	"object_id": true, "regex": true, "bson_timestamp": true,
}

func fpIsType(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
	if len(args) != 2 {
		return pval{}, fpErrorf("is_type(...) takes 2 arguments, got %d", len(args))
	}
	want, ok := fpStrOf(args[1])
	if !ok || !fpTypeNames[want] {
		return pval{}, fpErrorf("is_type(...) does not know the type %q", want)
	}
	if args[0].absent {
		return fpNull(), nil
	}
	got := fpTypeName(args[0])
	return fpBool(got == want || (want == "number" && fpKind(args[0].v) == fkNumber)), nil
}

func fpArith(op string) fpStrictFunc {
	return func(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
		if len(args) != 2 {
			return pval{}, fpErrorf("%s(...) takes 2 arguments, got %d", fn.Name, len(args))
		}
		a, b := args[0], args[1]
		if fpNullish(a) || fpNullish(b) {
			return fpNull(), nil
		}
		if fpKind(a.v) != fkNumber {
			return pval{}, fpTypeError(fn, a)
		}
		if fpKind(b.v) != fkNumber {
			return pval{}, fpTypeError(fn, b)
		}
		ai, aInt := fpIntOf(a.v)
		bi, bInt := fpIntOf(b.v)
		if aInt && bInt {
			var r int64
			switch op {
			case "add":
				r = ai + bi
				if (r > ai) != (bi > 0) {
					return pval{}, fpErrorf("add(...) overflows a 64-bit integer")
				}
			case "subtract":
				r = ai - bi
				if (r < ai) != (bi > 0) {
					return pval{}, fpErrorf("subtract(...) overflows a 64-bit integer")
				}
			case "multiply":
				r = ai * bi
				if ai != 0 && (r/ai != bi || (ai == -1 && bi == math.MinInt64) || (bi == -1 && ai == math.MinInt64)) {
					return pval{}, fpErrorf("multiply(...) overflows a 64-bit integer")
				}
			case "divide":
				if bi == 0 {
					return pval{}, fpErrorf("divide(...) by zero")
				}
				if ai == math.MinInt64 && bi == -1 {
					return pval{}, fpErrorf("divide(...) overflows a 64-bit integer")
				}
				r = ai / bi
			case "mod":
				if bi == 0 {
					return pval{}, fpErrorf("mod(...) by zero")
				}
				if bi == -1 {
					r = 0
				} else {
					r = ai % bi
				}
			}
			return fpInt(r), nil
		}
		af, _ := fpFloat(a.v)
		bf, _ := fpFloat(b.v)
		switch op {
		case "add":
			return fpDouble(af + bf), nil
		case "subtract":
			return fpDouble(af - bf), nil
		case "multiply":
			return fpDouble(af * bf), nil
		case "divide":
			return fpDouble(af / bf), nil
		}
		if bf == 0 {
			return fpDouble(math.NaN()), nil
		}
		return fpDouble(math.Mod(af, bf)), nil
	}
}

func fpNumericArg(fn *fsFunction, v pval) (pval, bool, error) {
	if fpNullish(v) {
		return fpNull(), false, nil
	}
	if fpKind(v.v) != fkNumber {
		return pval{}, false, fpTypeError(fn, v)
	}
	return v, true, nil
}

func fpAbs(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
	return fpUnary(fn, args, func(v pval) (pval, error) {
		v, ok, err := fpNumericArg(fn, v)
		if !ok {
			return v, err
		}
		if n, isInt := fpIntOf(v.v); isInt {
			if n == math.MinInt64 {
				return pval{}, fpErrorf("abs(...) overflows a 64-bit integer")
			}
			if n < 0 {
				n = -n
			}
			return fpInt(n), nil
		}
		f, _ := fpFloat(v.v)
		return fpDouble(math.Abs(f)), nil
	})
}

func fpRounding(f func(float64) float64) fpStrictFunc {
	return func(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
		return fpUnary(fn, args, func(v pval) (pval, error) {
			v, ok, err := fpNumericArg(fn, v)
			if !ok || fpIsInt(v.v) {
				return v, err
			}
			x, _ := fpFloat(v.v)
			return fpDouble(f(x)), nil
		})
	}
}

// fpRoundPlaces is round(...) and trunc(...): to a number of places right of
// the decimal point, or left of it when negative; round goes away from zero
// on a tie.
func fpRoundPlaces(round bool) fpStrictFunc {
	return func(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
		if len(args) < 1 || len(args) > 2 {
			return pval{}, fpErrorf("%s(...) takes 1 or 2 arguments, got %d", fn.Name, len(args))
		}
		v, ok, err := fpNumericArg(fn, args[0])
		if !ok {
			return v, err
		}
		places := int64(0)
		if len(args) == 2 {
			if fpNullish(args[1]) {
				return fpNull(), nil
			}
			p, isInt := fpIntOf(args[1].v)
			if !isInt {
				return pval{}, fpTypeError(fn, args[1])
			}
			places = p
		}
		apply := func(x float64) float64 {
			if round {
				return math.Round(x)
			}
			return math.Trunc(x)
		}
		if n, isInt := fpIntOf(v.v); isInt {
			if places >= 0 {
				return fpInt(n), nil
			}
			if places < -18 {
				return fpInt(0), nil
			}
			scale := int64(math.Pow10(int(-places)))
			q, r := n/scale, n%scale
			if round && 2*absInt64(r) >= scale {
				if n < 0 {
					q--
				} else {
					q++
				}
			}
			if q != 0 && (q*scale)/scale != q {
				return pval{}, fpErrorf("%s(...) overflows a 64-bit integer", fn.Name)
			}
			res := q * scale
			if (n > 0 && res < 0) || (n < 0 && res > 0) {
				return pval{}, fpErrorf("%s(...) overflows a 64-bit integer", fn.Name)
			}
			return fpInt(res), nil
		}
		x, _ := fpFloat(v.v)
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return fpDouble(x), nil
		}
		scale := math.Pow10(int(places))
		return fpDouble(apply(x*scale) / scale), nil
	}
}

func absInt64(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

func fpPow(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
	if len(args) != 2 {
		return pval{}, fpErrorf("pow(...) takes 2 arguments, got %d", len(args))
	}
	b, ok, err := fpNumericArg(fn, args[0])
	if !ok {
		return b, err
	}
	e, ok, err := fpNumericArg(fn, args[1])
	if !ok {
		return e, err
	}
	bf, _ := fpFloat(b.v)
	ef, _ := fpFloat(e.v)
	if bf == 1 || ef == 0 {
		return fpDouble(1), nil
	}
	if (bf < 0 && ef != math.Trunc(ef)) || (bf == 0 && ef < 0) {
		return pval{}, fpErrorf("pow(...) is undefined for base %v and exponent %v", bf, ef)
	}
	return fpDouble(math.Pow(bf, ef)), nil
}

func fpMathUnary(f func(float64) (float64, error)) fpStrictFunc {
	return func(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
		return fpUnary(fn, args, func(v pval) (pval, error) {
			v, ok, err := fpNumericArg(fn, v)
			if !ok {
				return v, err
			}
			x, _ := fpFloat(v.v)
			r, err := f(x)
			if err != nil {
				return pval{}, err
			}
			return fpDouble(r), nil
		})
	}
}

func fpLn(x float64) (float64, error) {
	if math.IsNaN(x) || math.IsInf(x, -1) {
		return math.NaN(), nil
	}
	if x <= 0 {
		return 0, fpErrorf("ln(...) of a number not above 0")
	}
	return math.Log(x), nil
}

func fpLog(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
	if len(args) < 1 || len(args) > 2 {
		return pval{}, fpErrorf("log(...) takes 1 or 2 arguments, got %d", len(args))
	}
	n, ok, err := fpNumericArg(fn, args[0])
	if !ok {
		return n, err
	}
	x, _ := fpFloat(n.v)
	if len(args) == 1 {
		r, err := fpLn(x)
		if err != nil {
			return pval{}, err
		}
		return fpDouble(r), nil
	}
	b, ok, err := fpNumericArg(fn, args[1])
	if !ok {
		return b, err
	}
	base, _ := fpFloat(b.v)
	if math.IsNaN(x) || math.IsNaN(base) || math.IsInf(x, -1) || math.IsInf(base, 1) {
		return fpDouble(math.NaN()), nil
	}
	if x <= 0 || base <= 0 || base == 1 {
		return pval{}, fpErrorf("log(...) is undefined for number %v and base %v", x, base)
	}
	return fpDouble(math.Log(x) / math.Log(base)), nil
}

func fpConcat(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
	if len(args) < 2 {
		return pval{}, fpErrorf("concat(...) takes two or more values")
	}
	for _, a := range args {
		if fpNullish(a) {
			return fpNull(), nil
		}
	}
	kind := fpKind(args[0].v)
	for _, a := range args {
		if fpKind(a.v) != kind {
			return pval{}, fpErrorf("concat(...) takes values of one type")
		}
	}
	switch kind {
	case fkString:
		var sb strings.Builder
		for _, a := range args {
			sb.WriteString(*a.v.StringValue)
		}
		return fpString(sb.String()), nil
	case fkBytes:
		var out []byte
		for _, a := range args {
			b, _ := fpBytesOf(a.v)
			out = append(out, b...)
		}
		return fpBytes(out), nil
	case fkArray:
		var out []FSValue
		for _, a := range args {
			out = append(out, a.v.ArrayValue.Values...)
		}
		return fpArray(out), nil
	}
	return pval{}, fpTypeError(fn, args[0])
}

func fpLength(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
	return fpUnary(fn, args, func(v pval) (pval, error) {
		if fpNullish(v) {
			return fpNull(), nil
		}
		switch fpKind(v.v) {
		case fkString:
			return fpInt(int64(utf8.RuneCountInString(*v.v.StringValue))), nil
		case fkBytes:
			b, _ := fpBytesOf(v.v)
			return fpInt(int64(len(b))), nil
		case fkArray:
			return fpInt(int64(len(v.v.ArrayValue.Values))), nil
		case fkVector:
			vec, _ := fpVectorOf(v.v)
			return fpInt(int64(len(vec))), nil
		case fkMap:
			return fpInt(int64(len(v.v.MapValue.Fields))), nil
		}
		return pval{}, fpTypeError(fn, v)
	})
}

func fpReverse(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
	return fpUnary(fn, args, func(v pval) (pval, error) {
		if fpNullish(v) {
			return fpNull(), nil
		}
		switch fpKind(v.v) {
		case fkString, fkBytes:
			return fpStringReverse(nil, nil, fn, args)
		case fkArray:
			return fpArrayReverse(nil, nil, fn, args)
		}
		return pval{}, fpTypeError(fn, v)
	})
}

// fpStrOrBytes reads a STRING or BYTES argument as bytes, reporting which.
func fpStrOrBytes(fn *fsFunction, v pval) (b []byte, isString bool, err error) {
	switch fpKind(v.v) {
	case fkString:
		return []byte(*v.v.StringValue), true, nil
	case fkBytes:
		raw, _ := fpBytesOf(v.v)
		return raw, false, nil
	}
	return nil, false, fpTypeError(fn, v)
}

func fpSameKind(isString bool, b []byte) pval {
	if isString {
		return fpString(string(b))
	}
	return fpBytes(b)
}

func fpByteLength(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
	return fpUnary(fn, args, func(v pval) (pval, error) {
		if fpNullish(v) {
			return fpNull(), nil
		}
		b, _, err := fpStrOrBytes(fn, v)
		if err != nil {
			return pval{}, err
		}
		return fpInt(int64(len(b))), nil
	})
}

func fpCharLength(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
	return fpUnary(fn, args, func(v pval) (pval, error) {
		if fpNullish(v) {
			return fpNull(), nil
		}
		s, ok := fpStrOf(v)
		if !ok {
			return pval{}, fpTypeError(fn, v)
		}
		return fpInt(int64(utf8.RuneCountInString(s))), nil
	})
}

func fpTwoStrings(fn *fsFunction, args []pval) (a, b string, null bool, err error) {
	if len(args) != 2 {
		return "", "", false, fpErrorf("%s(...) takes 2 arguments, got %d", fn.Name, len(args))
	}
	if fpNullish(args[0]) || fpNullish(args[1]) {
		return "", "", true, nil
	}
	a, ok := fpStrOf(args[0])
	if !ok {
		return "", "", false, fpTypeError(fn, args[0])
	}
	b, ok = fpStrOf(args[1])
	if !ok {
		return "", "", false, fpTypeError(fn, args[1])
	}
	return a, b, false, nil
}

func fpStrPredicate(f func(s, sub string) bool) fpStrictFunc {
	return func(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
		a, b, null, err := fpTwoStrings(fn, args)
		if err != nil || null {
			return fpNull(), err
		}
		return fpBool(f(a, b)), nil
	}
}

// fpLike matches SQL LIKE: % any run of characters, _ one character, and a
// backslash escapes the next character.
func fpLike(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
	value, pattern, null, err := fpTwoStrings(fn, args)
	if err != nil || null {
		return fpNull(), err
	}
	var re strings.Builder
	re.WriteString("(?s)^")
	runes := []rune(pattern)
	for i := 0; i < len(runes); i++ {
		switch c := runes[i]; {
		case c == '\\' && i+1 < len(runes):
			i++
			re.WriteString(regexp.QuoteMeta(string(runes[i])))
		case c == '%':
			re.WriteString(".*")
		case c == '_':
			re.WriteString(".")
		default:
			re.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	re.WriteString("$")
	compiled, err := regexp.Compile(re.String())
	if err != nil {
		return pval{}, fpErrorf("like(...) pattern %q: %v", pattern, err)
	}
	return fpBool(compiled.MatchString(value)), nil
}

// fpRegex runs the re2 regular expressions string-functions.md.gz names;
// Go's regexp package implements the re2 syntax.
func fpRegex(mode string) fpStrictFunc {
	return func(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
		value, pattern, null, err := fpTwoStrings(fn, args)
		if err != nil || null {
			return fpNull(), err
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return pval{}, fpErrorf("%s(...) pattern %q is not a valid regular expression", fn.Name, pattern)
		}
		switch mode {
		case "contains":
			return fpBool(re.MatchString(value)), nil
		case "match":
			full, err := regexp.Compile("^(?:" + pattern + ")$")
			if err != nil {
				return pval{}, fpErrorf("%s(...) pattern %q is not a valid regular expression", fn.Name, pattern)
			}
			return fpBool(full.MatchString(value)), nil
		case "find":
			loc := re.FindStringIndex(value)
			if loc == nil {
				return fpNull(), nil
			}
			return fpString(value[loc[0]:loc[1]]), nil
		}
		found := re.FindAllString(value, -1)
		out := make([]FSValue, 0, len(found))
		for _, f := range found {
			out = append(out, fpString(f).v)
		}
		return fpArray(out), nil
	}
}

func fpStringConcat(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
	if len(args) == 0 {
		return pval{}, fpErrorf("string_concat(...) takes one or more strings")
	}
	var sb strings.Builder
	for _, a := range args {
		if fpNullish(a) {
			return fpNull(), nil
		}
		s, ok := fpStrOf(a)
		if !ok {
			return pval{}, fpTypeError(fn, a)
		}
		sb.WriteString(s)
	}
	return fpString(sb.String()), nil
}

func fpStringIndexOf(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
	if len(args) != 2 {
		return pval{}, fpErrorf("string_index_of(...) takes 2 arguments, got %d", len(args))
	}
	if fpNullish(args[0]) || fpNullish(args[1]) {
		return fpNull(), nil
	}
	hay, isStr, err := fpStrOrBytes(fn, args[0])
	if err != nil {
		return pval{}, err
	}
	needle, needleStr, err := fpStrOrBytes(fn, args[1])
	if err != nil || needleStr != isStr {
		return pval{}, fpErrorf("string_index_of(...) takes two strings or two byte values")
	}
	i := bytes.Index(hay, needle)
	if i < 0 {
		return fpInt(-1), nil
	}
	if isStr {
		i = utf8.RuneCount(hay[:i])
	}
	return fpInt(int64(i)), nil
}

func fpCase(r func(rune) rune, b func([]byte) []byte) fpStrictFunc {
	return func(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
		return fpUnary(fn, args, func(v pval) (pval, error) {
			if fpNullish(v) {
				return fpNull(), nil
			}
			raw, isStr, err := fpStrOrBytes(fn, v)
			if err != nil {
				return pval{}, err
			}
			if isStr {
				return fpString(strings.Map(r, string(raw))), nil
			}
			return fpBytes(b(raw)), nil
		})
	}
}

func fpUnits(raw []byte, isStr bool) [][]byte {
	var out [][]byte
	if isStr {
		for _, r := range string(raw) {
			out = append(out, []byte(string(r)))
		}
		return out
	}
	for i := range raw {
		out = append(out, raw[i:i+1])
	}
	return out
}

func fpSubstring(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
	if len(args) < 2 || len(args) > 3 {
		return pval{}, fpErrorf("substring(...) takes 2 or 3 arguments, got %d", len(args))
	}
	for _, a := range args {
		if fpNullish(a) {
			return fpNull(), nil
		}
	}
	raw, isStr, err := fpStrOrBytes(fn, args[0])
	if err != nil {
		return pval{}, err
	}
	units := fpUnits(raw, isStr)
	pos, ok := fpIntOf(args[1].v)
	if !ok {
		return pval{}, fpTypeError(fn, args[1])
	}
	if pos < 0 {
		pos += int64(len(units))
		if pos < 0 {
			pos = 0
		}
	}
	end := int64(len(units))
	if len(args) == 3 {
		l, ok := fpIntOf(args[2].v)
		if !ok || l < 0 {
			return pval{}, fpErrorf("substring(...) takes a non-negative length")
		}
		if pos+l < end {
			end = pos + l
		}
	}
	if pos > int64(len(units)) {
		pos = int64(len(units))
	}
	if end < pos {
		end = pos
	}
	return fpSameKind(isStr, bytes.Join(units[pos:end], nil)), nil
}

func fpStringReverse(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
	return fpUnary(fn, args, func(v pval) (pval, error) {
		if fpNullish(v) {
			return fpNull(), nil
		}
		raw, isStr, err := fpStrOrBytes(fn, v)
		if err != nil {
			return pval{}, err
		}
		units := fpUnits(raw, isStr)
		for i, j := 0, len(units)-1; i < j; i, j = i+1, j-1 {
			units[i], units[j] = units[j], units[i]
		}
		return fpSameKind(isStr, bytes.Join(units, nil)), nil
	})
}

func fpStringRepeat(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
	if len(args) != 2 {
		return pval{}, fpErrorf("string_repeat(...) takes 2 arguments, got %d", len(args))
	}
	if fpNullish(args[0]) || fpNullish(args[1]) {
		return fpNull(), nil
	}
	raw, isStr, err := fpStrOrBytes(fn, args[0])
	if err != nil {
		return pval{}, err
	}
	n, ok := fpIntOf(args[1].v)
	if !ok || n < 0 {
		return pval{}, fpErrorf("string_repeat(...) takes a non-negative repetition count")
	}
	if int64(len(raw))*n > 1<<20 {
		return pval{}, fpErrorf("string_repeat(...) result exceeds 1 MB")
	}
	return fpSameKind(isStr, bytes.Repeat(raw, int(n))), nil
}

func fpStringReplace(limit int) fpStrictFunc {
	return func(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
		if len(args) != 3 {
			return pval{}, fpErrorf("%s(...) takes 3 arguments, got %d", fn.Name, len(args))
		}
		for _, a := range args {
			if fpNullish(a) {
				return fpNull(), nil
			}
		}
		in, isStr, err := fpStrOrBytes(fn, args[0])
		if err != nil {
			return pval{}, err
		}
		find, findStr, err := fpStrOrBytes(fn, args[1])
		if err != nil {
			return pval{}, err
		}
		repl, replStr, err := fpStrOrBytes(fn, args[2])
		if err != nil {
			return pval{}, err
		}
		if findStr != isStr || replStr != isStr {
			return pval{}, fpErrorf("%s(...) takes values of one type", fn.Name)
		}
		if len(find) == 0 {
			return args[0], nil
		}
		return fpSameKind(isStr, bytes.Replace(in, find, repl, limit)), nil
	}
}

func fpTrim(left, right bool) fpStrictFunc {
	return func(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
		if len(args) < 1 || len(args) > 2 {
			return pval{}, fpErrorf("%s(...) takes 1 or 2 arguments, got %d", fn.Name, len(args))
		}
		for _, a := range args {
			if fpNullish(a) {
				return fpNull(), nil
			}
		}
		in, isStr, err := fpStrOrBytes(fn, args[0])
		if err != nil {
			return pval{}, err
		}
		inUnits := fpUnits(in, isStr)
		cut := func(u []byte) bool {
			if isStr {
				r, _ := utf8.DecodeRune(u)
				return unicode.IsSpace(r)
			}
			return len(u) == 1 && (u[0] == ' ' || (u[0] >= '\t' && u[0] <= '\r'))
		}
		if len(args) == 2 {
			set, setStr, err := fpStrOrBytes(fn, args[1])
			if err != nil {
				return pval{}, err
			}
			if setStr != isStr {
				return pval{}, fpErrorf("%s(...) takes values of one type", fn.Name)
			}
			members := map[string]bool{}
			for _, u := range fpUnits(set, isStr) {
				members[string(u)] = true
			}
			cut = func(u []byte) bool { return members[string(u)] }
		}
		start, end := 0, len(inUnits)
		for left && start < end && cut(inUnits[start]) {
			start++
		}
		for right && end > start && cut(inUnits[end-1]) {
			end--
		}
		return fpSameKind(isStr, bytes.Join(inUnits[start:end], nil)), nil
	}
}

func fpSplit(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
	if len(args) < 1 || len(args) > 2 {
		return pval{}, fpErrorf("split(...) takes 1 or 2 arguments, got %d", len(args))
	}
	for _, a := range args {
		if fpNullish(a) {
			return fpNull(), nil
		}
	}
	in, isStr, err := fpStrOrBytes(fn, args[0])
	if err != nil {
		return pval{}, err
	}
	delim := []byte(",")
	if len(args) == 2 {
		d, dStr, err := fpStrOrBytes(fn, args[1])
		if err != nil {
			return pval{}, err
		}
		if dStr != isStr {
			return pval{}, fpErrorf("split(...) takes values of one type")
		}
		delim = d
	} else if !isStr {
		return pval{}, fpErrorf("split(...) of bytes needs a delimiter")
	}
	var parts [][]byte
	if len(delim) == 0 {
		parts = fpUnits(in, isStr)
	} else {
		parts = bytes.Split(in, delim)
	}
	out := make([]FSValue, 0, len(parts))
	for _, p := range parts {
		out = append(out, fpSameKind(isStr, p).v)
	}
	return fpArray(out), nil
}

func fpJoin(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
	if len(args) < 2 || len(args) > 3 {
		return pval{}, fpErrorf("join(...) takes 2 or 3 arguments, got %d", len(args))
	}
	if fpNullish(args[0]) || fpNullish(args[1]) {
		return fpNull(), nil
	}
	if args[0].v.ArrayValue == nil {
		return pval{}, fpTypeError(fn, args[0])
	}
	delim, isStr, err := fpStrOrBytes(fn, args[1])
	if err != nil {
		return pval{}, err
	}
	var nullText []byte
	hasNullText := len(args) == 3
	if hasNullText {
		nt, ntStr, err := fpStrOrBytes(fn, args[2])
		if err != nil || ntStr != isStr {
			return pval{}, fpErrorf("join(...) takes values of one type")
		}
		nullText = nt
	}
	var parts [][]byte
	for _, e := range args[0].v.ArrayValue.Values {
		if e.NullValue != nil {
			if hasNullText {
				parts = append(parts, nullText)
			}
			continue
		}
		b, eStr, err := fpStrOrBytes(fn, fpOf(e))
		if err != nil || eStr != isStr {
			return pval{}, fpErrorf("join(...) takes values of one type")
		}
		parts = append(parts, b)
	}
	return fpSameKind(isStr, bytes.Join(parts, delim)), nil
}

func fpArrayArg(fn *fsFunction, v pval) ([]FSValue, bool, error) {
	if fpNullish(v) {
		return nil, false, nil
	}
	if v.v.ArrayValue == nil {
		return nil, false, fpTypeError(fn, v)
	}
	return v.v.ArrayValue.Values, true, nil
}

func fpArrayConcat(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
	var out []FSValue
	for _, a := range args {
		vals, ok, err := fpArrayArg(fn, a)
		if !ok {
			return fpNull(), err
		}
		out = append(out, vals...)
	}
	return fpArray(out), nil
}

func fpArrayContains(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
	if len(args) != 2 {
		return pval{}, fpErrorf("array_contains(...) takes 2 arguments, got %d", len(args))
	}
	vals, ok, err := fpArrayArg(fn, args[0])
	if !ok {
		return fpNull(), err
	}
	for _, v := range vals {
		if !args[1].absent && fpEqual(v, args[1].v) {
			return fpBool(true), nil
		}
	}
	return fpBool(false), nil
}

func fpArrayContainsSet(all bool) fpStrictFunc {
	return func(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
		if len(args) != 2 {
			return pval{}, fpErrorf("%s(...) takes 2 arguments, got %d", fn.Name, len(args))
		}
		vals, ok, err := fpArrayArg(fn, args[0])
		if !ok {
			return fpNull(), err
		}
		search, ok, err := fpArrayArg(fn, args[1])
		if !ok {
			return fpNull(), err
		}
		for _, s := range search {
			found := false
			for _, v := range vals {
				if fpEqual(v, s) {
					found = true
					break
				}
			}
			if found && !all {
				return fpBool(true), nil
			}
			if !found && all {
				return fpBool(false), nil
			}
		}
		return fpBool(all), nil
	}
}

func fpArrayGet(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
	if len(args) != 2 {
		return pval{}, fpErrorf("%s(...) takes 2 arguments, got %d", fn.Name, len(args))
	}
	vals, ok, err := fpArrayArg(fn, args[0])
	if !ok {
		if err == nil && args[0].absent {
			return fpAbsent, nil
		}
		return fpNull(), err
	}
	i, isInt := fpIntOf(args[1].v)
	if args[1].absent || !isInt {
		return pval{}, fpTypeError(fn, args[1])
	}
	if i < 0 {
		i += int64(len(vals))
	}
	if i < 0 || i >= int64(len(vals)) {
		return fpAbsent, nil
	}
	return fpOf(vals[i]), nil
}

func fpArrayLength(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
	return fpUnary(fn, args, func(v pval) (pval, error) {
		vals, ok, err := fpArrayArg(fn, v)
		if !ok {
			return fpNull(), err
		}
		return fpInt(int64(len(vals))), nil
	})
}

func fpArrayReverse(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
	return fpUnary(fn, args, func(v pval) (pval, error) {
		vals, ok, err := fpArrayArg(fn, v)
		if !ok {
			return fpNull(), err
		}
		out := make([]FSValue, len(vals))
		for i, e := range vals {
			out[len(vals)-1-i] = e
		}
		return fpArray(out), nil
	})
}

func fpArrayEnd(first bool) fpStrictFunc {
	return func(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
		return fpUnary(fn, args, func(v pval) (pval, error) {
			vals, ok, err := fpArrayArg(fn, v)
			if !ok {
				return fpNull(), err
			}
			if len(vals) == 0 {
				return fpAbsent, nil
			}
			if first {
				return fpOf(vals[0]), nil
			}
			return fpOf(vals[len(vals)-1]), nil
		})
	}
}

func fpArrayEndN(first bool) fpStrictFunc {
	return func(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
		if len(args) != 2 {
			return pval{}, fpErrorf("%s(...) takes 2 arguments, got %d", fn.Name, len(args))
		}
		vals, ok, err := fpArrayArg(fn, args[0])
		if !ok {
			return fpNull(), err
		}
		n, isInt := fpIntOf(args[1].v)
		if !isInt || n < 0 {
			return pval{}, fpErrorf("%s(...) takes a non-negative count", fn.Name)
		}
		if n > int64(len(vals)) {
			n = int64(len(vals))
		}
		if first {
			return fpArray(append([]FSValue{}, vals[:n]...)), nil
		}
		return fpArray(append([]FSValue{}, vals[int64(len(vals))-n:]...)), nil
	}
}

// array_index_of's third argument, which the Go client sends as "first" or
// "last", picks the occurrence.
func fpArrayIndexOf(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
	if len(args) < 2 || len(args) > 3 {
		return pval{}, fpErrorf("array_index_of(...) takes 2 or 3 arguments, got %d", len(args))
	}
	vals, ok, err := fpArrayArg(fn, args[0])
	if !ok {
		return fpNull(), err
	}
	last := false
	if len(args) == 3 {
		which, _ := fpStrOf(args[2])
		switch which {
		case "first":
		case "last":
			last = true
		default:
			return pval{}, fpErrorf("array_index_of(...) occurrence is first or last, not %q", which)
		}
	}
	found := int64(-1)
	for i, v := range vals {
		if !args[1].absent && fpEqual(v, args[1].v) {
			found = int64(i)
			if !last {
				break
			}
		}
	}
	return fpInt(found), nil
}

func fpArrayIndexOfAll(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
	if len(args) != 2 {
		return pval{}, fpErrorf("array_index_of_all(...) takes 2 arguments, got %d", len(args))
	}
	vals, ok, err := fpArrayArg(fn, args[0])
	if !ok {
		return fpNull(), err
	}
	out := []FSValue{}
	for i, v := range vals {
		if !args[1].absent && fpEqual(v, args[1].v) {
			out = append(out, fpInt(int64(i)).v)
		}
	}
	return fpArray(out), nil
}

func fpArraySlice(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
	if len(args) != 3 {
		return pval{}, fpErrorf("array_slice(...) takes 3 arguments, got %d", len(args))
	}
	vals, ok, err := fpArrayArg(fn, args[0])
	if !ok {
		return fpNull(), err
	}
	off, okOff := fpIntOf(args[1].v)
	length, okLen := fpIntOf(args[2].v)
	if !okOff || !okLen || length < 0 {
		return pval{}, fpErrorf("array_slice(...) takes an integer offset and a non-negative length")
	}
	if off < 0 {
		off += int64(len(vals))
		if off < 0 {
			off = 0
		}
	}
	if off > int64(len(vals)) {
		off = int64(len(vals))
	}
	end := off + length
	if end > int64(len(vals)) {
		end = int64(len(vals))
	}
	return fpArray(append([]FSValue{}, vals[off:end]...)), nil
}

// fpArraySum is sum(array): its numeric elements, as an integer until a
// floating-point element or an overflow past one widens it.
func fpArraySum(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
	return fpUnary(fn, args, func(v pval) (pval, error) {
		vals, ok, err := fpArrayArg(fn, v)
		if !ok {
			return fpNull(), err
		}
		var nums []FSValue
		for _, e := range vals {
			if fpKind(e) == fkNumber {
				nums = append(nums, e)
			}
		}
		return fpSumValues(nums)
	})
}

func fpSumValues(nums []FSValue) (pval, error) {
	if len(nums) == 0 {
		return fpNull(), nil
	}
	isInt := true
	var isum int64
	var fsum float64
	for _, n := range nums {
		if i, ok := fpIntOf(n); ok && isInt {
			s := isum + i
			if (s > isum) != (i > 0) && i != 0 {
				return pval{}, fpErrorf("sum(...) overflows a 64-bit integer")
			}
			isum = s
			continue
		}
		if isInt {
			fsum, isInt = float64(isum), false
		}
		f, _ := fpFloat(n)
		fsum += f
	}
	if isInt {
		return fpInt(isum), nil
	}
	return fpDouble(fsum), nil
}

func fpMapFunc(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
	if len(args)%2 != 0 {
		return pval{}, fpErrorf("map(...) takes key-value pairs")
	}
	out := map[string]FSValue{}
	for i := 0; i < len(args); i += 2 {
		k, ok := fpStrOf(args[i])
		if !ok {
			return pval{}, fpErrorf("map(...) keys are strings")
		}
		if !args[i+1].absent {
			out[k] = args[i+1].v
		}
	}
	return fpMap(out), nil
}

func fpMapGet(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
	if len(args) != 2 {
		return pval{}, fpErrorf("%s(...) takes 2 arguments, got %d", fn.Name, len(args))
	}
	key, ok := fpStrOf(args[1])
	if !ok {
		return pval{}, fpErrorf("%s(...) takes a string key", fn.Name)
	}
	if args[0].absent || args[0].v.MapValue == nil {
		return fpAbsent, nil
	}
	v, ok := args[0].v.MapValue.Fields[key]
	if !ok {
		return fpAbsent, nil
	}
	return fpOf(v), nil
}

func fpMapCopy(v pval) map[string]FSValue {
	out := map[string]FSValue{}
	for k, e := range v.v.MapValue.Fields {
		out[k] = e
	}
	return out
}

func fpMapSet(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
	if len(args) < 3 || len(args)%2 != 1 {
		return pval{}, fpErrorf("map_set(...) takes a map and key-value pairs")
	}
	if args[0].absent || args[0].v.MapValue == nil {
		return fpAbsent, nil
	}
	out := fpMapCopy(args[0])
	for i := 1; i < len(args); i += 2 {
		k, ok := fpStrOf(args[i])
		if !ok {
			return pval{}, fpErrorf("map_set(...) keys are strings")
		}
		if args[i+1].absent {
			delete(out, k)
		} else {
			out[k] = args[i+1].v
		}
	}
	return fpMap(out), nil
}

func fpMapRemove(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
	if len(args) < 2 {
		return pval{}, fpErrorf("map_remove(...) takes a map and keys")
	}
	if fpNullish(args[0]) {
		return fpNull(), nil
	}
	if args[0].v.MapValue == nil {
		return pval{}, fpTypeError(fn, args[0])
	}
	out := fpMapCopy(args[0])
	for _, a := range args[1:] {
		k, ok := fpStrOf(a)
		if !ok {
			return pval{}, fpErrorf("map_remove(...) keys are strings")
		}
		delete(out, k)
	}
	return fpMap(out), nil
}

func fpMapMerge(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
	if len(args) < 2 {
		return pval{}, fpErrorf("map_merge(...) takes two or more maps")
	}
	out := map[string]FSValue{}
	for _, a := range args {
		if fpNullish(a) {
			return fpNull(), nil
		}
		if a.v.MapValue == nil {
			return pval{}, fpTypeError(fn, a)
		}
		for k, v := range a.v.MapValue.Fields {
			out[k] = v
		}
	}
	return fpMap(out), nil
}

func fpMapParts(part string) fpStrictFunc {
	return func(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
		return fpUnary(fn, args, func(v pval) (pval, error) {
			if fpNullish(v) {
				return fpNull(), nil
			}
			if v.v.MapValue == nil {
				return pval{}, fpTypeError(fn, v)
			}
			out := []FSValue{}
			for _, k := range fpSortedKeys(v.v.MapValue.Fields) {
				e := v.v.MapValue.Fields[k]
				switch part {
				case "keys":
					out = append(out, fpString(k).v)
				case "values":
					out = append(out, e)
				default:
					out = append(out, fpMap(map[string]FSValue{"k": fpString(k).v, "v": e}).v)
				}
			}
			return fpArray(out), nil
		})
	}
}

// fpRefSegments splits a reference into its path segments below the
// database's documents.
func fpRefSegments(ref string) []string {
	p := fpReferencePath(ref)
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

func fpRefOf(env *fpEnv, segs []string) pval {
	if len(segs) == 0 {
		return pval{v: FSValue{ReferenceValue: env.documentsRoot()}}
	}
	return pval{v: FSValue{ReferenceValue: env.documentsRoot() + "/" + strings.Join(segs, "/")}}
}

func fpRefPart(part string) fpStrictFunc {
	return func(env *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
		return fpUnary(fn, args, func(v pval) (pval, error) {
			if fpNullish(v) {
				return fpNull(), nil
			}
			if v.v.ReferenceValue == "" {
				return pval{}, fpTypeError(fn, v)
			}
			segs := fpRefSegments(v.v.ReferenceValue)
			switch part {
			case "collection_id":
				if len(segs) < 2 {
					return fpNull(), nil
				}
				return fpString(segs[len(segs)-2]), nil
			case "document_id":
				if len(segs) < 2 {
					return fpNull(), nil
				}
				return fpString(segs[len(segs)-1]), nil
			}
			if len(segs) == 0 {
				return fpNull(), nil
			}
			return fpRefOf(env, segs[:len(segs)-2]), nil
		})
	}
}

// fpReferenceSlice slices a reference by (collection, document) pairs, as
// reference-functions.md.gz's examples count segments.
func fpReferenceSlice(env *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
	if len(args) < 2 || len(args) > 3 {
		return pval{}, fpErrorf("reference_slice(...) takes 2 or 3 arguments, got %d", len(args))
	}
	if fpNullish(args[0]) {
		return fpNull(), nil
	}
	if args[0].v.ReferenceValue == "" {
		return pval{}, fpTypeError(fn, args[0])
	}
	segs := fpRefSegments(args[0].v.ReferenceValue)
	pairs := len(segs) / 2
	off, ok := fpIntOf(args[1].v)
	if !ok {
		return pval{}, fpTypeError(fn, args[1])
	}
	if off < 0 {
		off += int64(pairs)
		if off < 0 {
			off = 0
		}
	}
	if off > int64(pairs) {
		off = int64(pairs)
	}
	end := int64(pairs)
	if len(args) == 3 {
		l, ok := fpIntOf(args[2].v)
		if !ok || l < 0 {
			return pval{}, fpErrorf("reference_slice(...) takes a non-negative length")
		}
		if off+l < end {
			end = off + l
		}
	}
	return fpRefOf(env, segs[2*off:2*end]), nil
}

func fpVectorFunc(measure string) fpStrictFunc {
	return func(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
		if len(args) != 2 {
			return pval{}, fpErrorf("%s(...) takes 2 arguments, got %d", fn.Name, len(args))
		}
		if fpNullish(args[0]) || fpNullish(args[1]) {
			return fpNull(), nil
		}
		a, okA := fpVectorOf(args[0].v)
		b, okB := fpVectorOf(args[1].v)
		if !okA || !okB {
			return pval{}, fpErrorf("%s(...) takes two vectors", fn.Name)
		}
		if len(a) != len(b) {
			return pval{}, fpErrorf("%s(...) takes vectors of one dimension", fn.Name)
		}
		d, err := fpVectorDistance(measure, a, b)
		if err != nil {
			return pval{}, err
		}
		return fpDouble(d), nil
	}
}

func fpVectorLength(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
	return fpUnary(fn, args, func(v pval) (pval, error) {
		if fpNullish(v) {
			return fpNull(), nil
		}
		vec, ok := fpVectorOf(v.v)
		if !ok {
			return pval{}, fpTypeError(fn, v)
		}
		return fpInt(int64(len(vec))), nil
	})
}

var fpTimestampMin = time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)
var fpTimestampMax = time.Date(9999, 12, 31, 23, 59, 59, 999999000, time.UTC)

func fpTimestampInRange(fn *fsFunction, t time.Time) (pval, error) {
	if t.Before(fpTimestampMin) || t.After(fpTimestampMax) {
		return pval{}, fpErrorf("%s(...) leaves the timestamp range", fn.Name)
	}
	return fpTimestamp(t), nil
}

func fpUnixToTimestamp(unit time.Duration) fpStrictFunc {
	return func(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
		return fpUnary(fn, args, func(v pval) (pval, error) {
			if fpNullish(v) {
				return fpNull(), nil
			}
			n, ok := fpIntOf(v.v)
			if !ok {
				return pval{}, fpTypeError(fn, v)
			}
			per := int64(time.Second / unit)
			sec, rem := n/per, n%per
			if rem < 0 {
				sec--
				rem += per
			}
			if sec < fpTimestampMin.Unix() || sec > fpTimestampMax.Unix() {
				return pval{}, fpErrorf("%s(...) leaves the timestamp range", fn.Name)
			}
			return fpTimestampInRange(fn, time.Unix(sec, rem*int64(unit)).UTC())
		})
	}
}

func fpTimestampToUnix(unit time.Duration) fpStrictFunc {
	return func(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
		return fpUnary(fn, args, func(v pval) (pval, error) {
			if fpNullish(v) {
				return fpNull(), nil
			}
			t, ok := fpTimeOf(v.v)
			if !ok {
				return pval{}, fpTypeError(fn, v)
			}
			per := int64(time.Second / unit)
			n := t.Unix()*per + int64(t.Nanosecond())/int64(unit)
			return fpInt(n), nil
		})
	}
}

var fpTimeUnits = map[string]time.Duration{
	"microsecond": time.Microsecond, "millisecond": time.Millisecond, "second": time.Second,
	"minute": time.Minute, "hour": time.Hour, "day": 24 * time.Hour,
}

func fpTimestampAdd(sign int64) fpStrictFunc {
	return func(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
		if len(args) != 3 {
			return pval{}, fpErrorf("%s(...) takes 3 arguments, got %d", fn.Name, len(args))
		}
		for _, a := range args {
			if fpNullish(a) {
				return fpNull(), nil
			}
		}
		t, ok := fpTimeOf(args[0].v)
		if !ok {
			return pval{}, fpTypeError(fn, args[0])
		}
		u, _ := fpStrOf(args[1])
		unit, ok := fpTimeUnits[u]
		if !ok {
			return pval{}, fpErrorf("%s(...) unit %q is not one of microsecond, millisecond, second, minute, hour, day", fn.Name, u)
		}
		amount, ok := fpIntOf(args[2].v)
		if !ok {
			return pval{}, fpTypeError(fn, args[2])
		}
		limit := math.MaxInt64 / int64(unit)
		if amount > limit || amount < -limit {
			return pval{}, fpErrorf("%s(...) leaves the timestamp range", fn.Name)
		}
		return fpTimestampInRange(fn, t.Add(time.Duration(sign*amount)*unit))
	}
}

func fpTimestampDiff(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
	if len(args) != 3 {
		return pval{}, fpErrorf("timestamp_diff(...) takes 3 arguments, got %d", len(args))
	}
	for _, a := range args {
		if fpNullish(a) {
			return fpNull(), nil
		}
	}
	end, ok1 := fpTimeOf(args[0].v)
	start, ok2 := fpTimeOf(args[1].v)
	if !ok1 || !ok2 {
		return pval{}, fpErrorf("timestamp_diff(...) takes two timestamps")
	}
	u, _ := fpStrOf(args[2])
	unit, ok := fpTimeUnits[u]
	if !ok {
		return pval{}, fpErrorf("timestamp_diff(...) unit %q is not one of microsecond, millisecond, second, minute, hour, day", u)
	}
	micros := end.Sub(start).Microseconds()
	return fpInt(micros / int64(unit/time.Microsecond)), nil
}

// fpZone reads a timestamp function's timezone: a tz database name, or an
// offset from GMT such as GMT+06:32:43.
func fpZone(fn *fsFunction, args []pval, i int) (*time.Location, error) {
	if len(args) <= i {
		return time.UTC, nil
	}
	name, ok := fpStrOf(args[i])
	if !ok {
		return nil, fpTypeError(fn, args[i])
	}
	if rest, isGMT := strings.CutPrefix(name, "GMT"); isGMT && rest != "" {
		sign := 1
		switch rest[0] {
		case '+':
		case '-':
			sign = -1
		default:
			return nil, fpErrorf("%s(...) timezone %q is not valid", fn.Name, name)
		}
		parts := strings.Split(rest[1:], ":")
		secs := 0
		for i, p := range parts {
			n, err := strconv.Atoi(p)
			if err != nil || i > 2 {
				return nil, fpErrorf("%s(...) timezone %q is not valid", fn.Name, name)
			}
			secs += n * []int{3600, 60, 1}[i]
		}
		return time.FixedZone(name, sign*secs), nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fpErrorf("%s(...) timezone %q is not valid", fn.Name, name)
	}
	return loc, nil
}

var fpWeekdays = map[string]time.Weekday{
	"sunday": time.Sunday, "monday": time.Monday, "tuesday": time.Tuesday, "wednesday": time.Wednesday,
	"thursday": time.Thursday, "friday": time.Friday, "saturday": time.Saturday,
}

// fpWeekStart reads week or week(<weekday>), whose weeks start on Sunday
// unless a weekday is named.
func fpWeekStart(g string) (time.Weekday, bool) {
	if g == "week" {
		return time.Sunday, true
	}
	if day, ok := strings.CutPrefix(g, "week("); ok && strings.HasSuffix(day, ")") {
		wd, known := fpWeekdays[strings.ToLower(strings.TrimSuffix(day, ")"))]
		return wd, known
	}
	return 0, false
}

func fpTimestampTrunc(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
	if len(args) < 2 || len(args) > 3 {
		return pval{}, fpErrorf("timestamp_trunc(...) takes 2 or 3 arguments, got %d", len(args))
	}
	if fpNullish(args[0]) || fpNullish(args[1]) {
		return fpNull(), nil
	}
	t, ok := fpTimeOf(args[0].v)
	if !ok {
		return pval{}, fpTypeError(fn, args[0])
	}
	g, _ := fpStrOf(args[1])
	loc, err := fpZone(fn, args, 2)
	if err != nil {
		return pval{}, err
	}
	lt := t.In(loc)
	day := time.Date(lt.Year(), lt.Month(), lt.Day(), 0, 0, 0, 0, loc)
	var out time.Time
	switch g {
	case "microsecond":
		out = t.Truncate(time.Microsecond)
	case "millisecond":
		out = t.Truncate(time.Millisecond)
	case "second":
		out = time.Date(lt.Year(), lt.Month(), lt.Day(), lt.Hour(), lt.Minute(), lt.Second(), 0, loc)
	case "minute":
		out = time.Date(lt.Year(), lt.Month(), lt.Day(), lt.Hour(), lt.Minute(), 0, 0, loc)
	case "hour":
		out = time.Date(lt.Year(), lt.Month(), lt.Day(), lt.Hour(), 0, 0, 0, loc)
	case "day":
		out = day
	case "month":
		out = time.Date(lt.Year(), lt.Month(), 1, 0, 0, 0, 0, loc)
	case "quarter":
		out = time.Date(lt.Year(), ((lt.Month()-1)/3)*3+1, 1, 0, 0, 0, 0, loc)
	case "year":
		out = time.Date(lt.Year(), 1, 1, 0, 0, 0, 0, loc)
	case "isoyear":
		y, _ := lt.ISOWeek()
		jan4 := time.Date(y, 1, 4, 0, 0, 0, 0, loc)
		out = jan4.AddDate(0, 0, -((int(jan4.Weekday()) + 6) % 7))
	default:
		start, ok := fpWeekStart(g)
		if !ok {
			return pval{}, fpErrorf("timestamp_trunc(...) granularity %q is not valid", g)
		}
		out = day.AddDate(0, 0, -((int(day.Weekday()) - int(start) + 7) % 7))
	}
	return fpTimestamp(out), nil
}

func fpTimestampExtract(_ *fpEnv, _ fpRow, fn *fsFunction, args []pval) (pval, error) {
	if len(args) < 2 || len(args) > 3 {
		return pval{}, fpErrorf("timestamp_extract(...) takes 2 or 3 arguments, got %d", len(args))
	}
	if fpNullish(args[0]) || fpNullish(args[1]) {
		return fpNull(), nil
	}
	t, ok := fpTimeOf(args[0].v)
	if !ok {
		return pval{}, fpTypeError(fn, args[0])
	}
	part, _ := fpStrOf(args[1])
	loc, err := fpZone(fn, args, 2)
	if err != nil {
		return pval{}, err
	}
	lt := t.In(loc)
	var n int
	switch part {
	case "microsecond":
		n = lt.Nanosecond() / 1000
	case "millisecond":
		n = lt.Nanosecond() / 1000000
	case "second":
		n = lt.Second()
	case "minute":
		n = lt.Minute()
	case "hour":
		n = lt.Hour()
	case "day":
		n = lt.Day()
	case "dayofweek":
		n = int(lt.Weekday()) + 1
	case "dayofyear":
		n = lt.YearDay()
	case "month":
		n = int(lt.Month())
	case "quarter":
		n = (int(lt.Month())-1)/3 + 1
	case "year":
		n = lt.Year()
	case "isoweek":
		_, n = lt.ISOWeek()
	case "isoyear":
		n, _ = lt.ISOWeek()
	default:
		start, ok := fpWeekStart(part)
		if !ok {
			return pval{}, fpErrorf("timestamp_extract(...) part %q is not valid", part)
		}
		// Week 1 begins on the year's first start weekday; days before it are
		// week 0.
		jan1 := time.Date(lt.Year(), 1, 1, 0, 0, 0, 0, loc)
		first := (int(start) - int(jan1.Weekday()) + 7) % 7
		n = (lt.YearDay()-1-first)/7 + 1
		if lt.YearDay()-1 < first {
			n = 0
		}
	}
	return fpInt(int64(n)), nil
}

func fpArrayFilter(env *fpEnv, row fpRow, fn *fsFunction) (pval, error) {
	if err := fpWantArgs(fn, 3, 3); err != nil {
		return pval{}, err
	}
	arr, err := fpEval(env, row, fn.Args[0])
	if err != nil {
		return pval{}, err
	}
	vals, ok, err := fpArrayArg(fn, arr)
	if !ok {
		return fpNull(), err
	}
	param, ok := fpStrOf(fpOf(fn.Args[1]))
	if !ok {
		return pval{}, fpErrorf("array_filter(...) takes the parameter's name as a string")
	}
	out := []FSValue{}
	for _, e := range vals {
		v, err := fpEval(env.withVars(map[string]pval{param: fpOf(e)}), row, fn.Args[2])
		if err != nil {
			return pval{}, err
		}
		if fpNullish(v) {
			continue
		}
		b, isBool := fpBoolOf(v)
		if !isBool {
			return pval{}, fpErrorf("array_filter(...) predicate evaluated to a %s", fpTypeName(v))
		}
		if b {
			out = append(out, e)
		}
	}
	return fpArray(out), nil
}

func fpArrayTransform(env *fpEnv, row fpRow, fn *fsFunction) (pval, error) {
	if err := fpWantArgs(fn, 3, 4); err != nil {
		return pval{}, err
	}
	arr, err := fpEval(env, row, fn.Args[0])
	if err != nil {
		return pval{}, err
	}
	vals, ok, err := fpArrayArg(fn, arr)
	if !ok {
		return fpNull(), err
	}
	param, ok := fpStrOf(fpOf(fn.Args[1]))
	if !ok {
		return pval{}, fpErrorf("array_transform(...) takes the parameter's name as a string")
	}
	indexParam, body := "", fn.Args[2]
	if len(fn.Args) == 4 {
		indexParam, ok = fpStrOf(fpOf(fn.Args[2]))
		if !ok {
			return pval{}, fpErrorf("array_transform(...) takes the index parameter's name as a string")
		}
		body = fn.Args[3]
	}
	out := make([]FSValue, 0, len(vals))
	for i, e := range vals {
		bind := map[string]pval{param: fpOf(e)}
		if indexParam != "" {
			bind[indexParam] = fpInt(int64(i))
		}
		v, err := fpEval(env.withVars(bind), row, body)
		if err != nil {
			return pval{}, err
		}
		if v.absent {
			v = fpNull()
		}
		out = append(out, v.v)
	}
	return fpArray(out), nil
}

// fpArrayFunc is array(...): an array of its arguments, an absent one as NULL
// (array-functions.md.gz), or, given a pipeline, the sub-pipeline's results
// as an array of maps.
func fpArrayFunc(env *fpEnv, row fpRow, fn *fsFunction) (pval, error) {
	if len(fn.Args) == 1 && fn.Args[0].PipelineValue != nil {
		rows, err := fpSubPipeline(env, row, fn.Args[0].PipelineValue)
		if err != nil {
			return pval{}, err
		}
		out := make([]FSValue, 0, len(rows))
		for _, r := range rows {
			out = append(out, fpMap(fpDocumentOf(r).Fields).v)
		}
		return fpArray(out), nil
	}
	args, err := fpArgs(env, row, fn)
	if err != nil {
		return pval{}, err
	}
	out := make([]FSValue, 0, len(args))
	for _, a := range args {
		if a.absent {
			a = fpNull()
		}
		out = append(out, a.v)
	}
	return fpArray(out), nil
}

// fpScalarFunc is scalar(...): the single value a sub-pipeline produces, the
// lone field of its lone result, or NULL when it produces none.
func fpScalarFunc(env *fpEnv, row fpRow, fn *fsFunction) (pval, error) {
	if len(fn.Args) != 1 || fn.Args[0].PipelineValue == nil {
		return pval{}, fpErrorf("scalar(...) takes a pipeline")
	}
	rows, err := fpSubPipeline(env, row, fn.Args[0].PipelineValue)
	if err != nil {
		return pval{}, err
	}
	switch len(rows) {
	case 0:
		return fpNull(), nil
	case 1:
	default:
		return pval{}, fpErrorf("scalar(...) sub-pipeline produced %d results, not one", len(rows))
	}
	fields := fpDocumentOf(rows[0]).Fields
	if len(fields) != 1 {
		return fpMap(fields), nil
	}
	for _, v := range fields {
		return fpOf(v), nil
	}
	return fpNull(), nil
}

// fpSubPipeline runs a nested pipeline in the scope of the row that holds it:
// a leading subcollection(...) reads the collection under the row's document,
// and the variables in scope stay visible (let-stage.md.gz).
func fpSubPipeline(env *fpEnv, row fpRow, p *fsPipeline) ([]fpRow, error) {
	if len(p.Stages) == 0 {
		return nil, fpErrorf("a sub-pipeline needs at least one stage")
	}
	inner := env.rowEnv(row)
	first := p.Stages[0]
	if first.Name != "subcollection" {
		return fpExecute(inner, p, nil)
	}
	name := row[fpNameField].ReferenceValue
	if name == "" || len(first.Args) != 1 || first.Args[0].StringValue == nil {
		return nil, fpErrorf("subcollection(...) runs under a document and takes the collection ID")
	}
	prefix := name + "/" + *first.Args[0].StringValue + "/"
	input := fpDocs(fsDocumentsUnder(prefix, func(d FSDocument) bool {
		return !strings.Contains(strings.TrimPrefix(d.Name, prefix), "/")
	}))
	rest := &fsPipeline{Stages: p.Stages[1:]}
	if len(rest.Stages) == 0 {
		return input, nil
	}
	return fpExecute(inner, rest, input)
}

// The aggregate functions of aggregate-functions.md.gz.
var fpAccumulators = map[string]bool{
	"count": true, "count_if": true, "count_distinct": true, "sum": true, "average": true,
	"minimum": true, "maximum": true, "first": true, "last": true, "array_agg": true, "array_agg_distinct": true,
}

func fpAccumulate(env *fpEnv, rows []fpRow, fn *fsFunction) (pval, error) {
	if !fpAccumulators[fn.Name] {
		return pval{}, fpErrorf("%s(...) is not an aggregate function", fn.Name)
	}
	if fn.Name == "count" && len(fn.Args) == 0 {
		return fpInt(int64(len(rows))), nil
	}
	if len(fn.Args) != 1 {
		return pval{}, fpErrorf("%s(...) takes 1 argument, got %d", fn.Name, len(fn.Args))
	}
	values := make([]pval, 0, len(rows))
	for _, row := range rows {
		v, err := fpEval(env.rowEnv(row), row, fn.Args[0])
		if err != nil {
			return pval{}, err
		}
		values = append(values, v)
	}
	switch fn.Name {
	case "count":
		n := 0
		for _, v := range values {
			if !fpNullish(v) {
				n++
			}
		}
		return fpInt(int64(n)), nil
	case "count_if":
		n := 0
		for _, v := range values {
			if b, ok := fpBoolOf(v); ok && b {
				n++
			}
		}
		return fpInt(int64(n)), nil
	case "count_distinct":
		var seen []FSValue
		for _, v := range values {
			if fpNullish(v) {
				continue
			}
			dup := false
			for _, s := range seen {
				if fpEqual(s, v.v) {
					dup = true
					break
				}
			}
			if !dup {
				seen = append(seen, v.v)
			}
		}
		return fpInt(int64(len(seen))), nil
	case "sum", "average":
		var nums []FSValue
		for _, v := range values {
			if !v.absent && fpKind(v.v) == fkNumber {
				if f, _ := fpFloat(v.v); math.IsNaN(f) {
					return fpDouble(math.NaN()), nil
				}
				nums = append(nums, v.v)
			}
		}
		if fn.Name == "sum" {
			return fpSumValues(nums)
		}
		if len(nums) == 0 {
			return fpNull(), nil
		}
		var total float64
		for _, n := range nums {
			f, _ := fpFloat(n)
			total += f
		}
		return fpDouble(total / float64(len(nums))), nil
	case "minimum", "maximum":
		sign := 1
		if fn.Name == "minimum" {
			sign = -1
		}
		var best *FSValue
		for _, v := range values {
			if fpNullish(v) {
				continue
			}
			if best == nil || fpCompare(v.v, *best)*sign > 0 {
				vv := v.v
				best = &vv
			}
		}
		if best == nil {
			return fpNull(), nil
		}
		return fpOf(*best), nil
	case "first", "last":
		if len(values) == 0 {
			return fpNull(), nil
		}
		v := values[0]
		if fn.Name == "last" {
			v = values[len(values)-1]
		}
		return v, nil
	}
	out := []FSValue{}
	for _, v := range values {
		if v.absent {
			v = fpNull()
		}
		if fn.Name == "array_agg_distinct" {
			dup := false
			for _, o := range out {
				if fpEqual(o, v.v) {
					dup = true
					break
				}
			}
			if dup {
				continue
			}
		}
		out = append(out, v.v)
	}
	return fpArray(out), nil
}
