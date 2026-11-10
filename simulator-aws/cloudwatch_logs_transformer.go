package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type cwtAddKeyEntry struct {
	Key               string `json:"key"`
	Value             string `json:"value"`
	OverwriteIfExists bool   `json:"overwriteIfExists,omitempty"`
}

type cwtAddKeys struct {
	Entries []cwtAddKeyEntry `json:"entries"`
}

type cwtCopyValueEntry struct {
	Source            string `json:"source"`
	Target            string `json:"target"`
	OverwriteIfExists bool   `json:"overwriteIfExists,omitempty"`
}

type cwtCopyValue struct {
	Entries []cwtCopyValueEntry `json:"entries"`
}

type cwtCSV struct {
	QuoteCharacter *string  `json:"quoteCharacter,omitempty"`
	Delimiter      *string  `json:"delimiter,omitempty"`
	Columns        []string `json:"columns,omitempty"`
	Source         *string  `json:"source,omitempty"`
	Destination    *string  `json:"destination,omitempty"`
}

type cwtDateTimeConverter struct {
	Source         string   `json:"source"`
	Target         string   `json:"target"`
	TargetFormat   *string  `json:"targetFormat,omitempty"`
	MatchPatterns  []string `json:"matchPatterns"`
	SourceTimezone *string  `json:"sourceTimezone,omitempty"`
	TargetTimezone *string  `json:"targetTimezone,omitempty"`
	Locale         *string  `json:"locale,omitempty"`
}

type cwtWithKeys struct {
	WithKeys []string `json:"withKeys"`
}

type cwtGrok struct {
	Source *string `json:"source,omitempty"`
	Match  string  `json:"match"`
}

type cwtListToMap struct {
	Source           string  `json:"source"`
	Key              string  `json:"key"`
	ValueKey         *string `json:"valueKey,omitempty"`
	Target           *string `json:"target,omitempty"`
	Flatten          bool    `json:"flatten,omitempty"`
	FlattenedElement *string `json:"flattenedElement,omitempty"`
}

type cwtMoveKeyEntry struct {
	Source            string `json:"source"`
	Target            string `json:"target"`
	OverwriteIfExists bool   `json:"overwriteIfExists,omitempty"`
}

type cwtMoveKeys struct {
	Entries []cwtMoveKeyEntry `json:"entries"`
}

type cwtSourceOnly struct {
	Source *string `json:"source,omitempty"`
}

type cwtParseJSON struct {
	Source      *string `json:"source,omitempty"`
	Destination *string `json:"destination,omitempty"`
}

type cwtParseKeyValue struct {
	Source            *string `json:"source,omitempty"`
	Destination       *string `json:"destination,omitempty"`
	FieldDelimiter    *string `json:"fieldDelimiter,omitempty"`
	KeyValueDelimiter *string `json:"keyValueDelimiter,omitempty"`
	KeyPrefix         *string `json:"keyPrefix,omitempty"`
	NonMatchValue     *string `json:"nonMatchValue,omitempty"`
	OverwriteIfExists bool    `json:"overwriteIfExists,omitempty"`
}

type cwtParseToOCSF struct {
	Source         *string `json:"source,omitempty"`
	EventSource    string  `json:"eventSource"`
	OcsfVersion    string  `json:"ocsfVersion"`
	MappingVersion *string `json:"mappingVersion,omitempty"`
}

type cwtRenameKeyEntry struct {
	Key               string `json:"key"`
	RenameTo          string `json:"renameTo"`
	OverwriteIfExists bool   `json:"overwriteIfExists,omitempty"`
}

type cwtRenameKeys struct {
	Entries []cwtRenameKeyEntry `json:"entries"`
}

type cwtSplitStringEntry struct {
	Source    string `json:"source"`
	Delimiter string `json:"delimiter"`
}

type cwtSplitString struct {
	Entries []cwtSplitStringEntry `json:"entries"`
}

type cwtSubstituteStringEntry struct {
	Source string `json:"source"`
	From   string `json:"from"`
	To     string `json:"to"`
}

type cwtSubstituteString struct {
	Entries []cwtSubstituteStringEntry `json:"entries"`
}

type cwtTypeConverterEntry struct {
	Key  string `json:"key"`
	Type string `json:"type"`
}

type cwtTypeConverter struct {
	Entries []cwtTypeConverterEntry `json:"entries"`
}

// cwtProcessor is the Processor structure of the CloudWatch Logs model; a
// processor sets exactly one member.
type cwtProcessor struct {
	AddKeys           *cwtAddKeys           `json:"addKeys,omitempty"`
	CopyValue         *cwtCopyValue         `json:"copyValue,omitempty"`
	CSV               *cwtCSV               `json:"csv,omitempty"`
	DateTimeConverter *cwtDateTimeConverter `json:"dateTimeConverter,omitempty"`
	DeleteKeys        *cwtWithKeys          `json:"deleteKeys,omitempty"`
	Grok              *cwtGrok              `json:"grok,omitempty"`
	ListToMap         *cwtListToMap         `json:"listToMap,omitempty"`
	LowerCaseString   *cwtWithKeys          `json:"lowerCaseString,omitempty"`
	MoveKeys          *cwtMoveKeys          `json:"moveKeys,omitempty"`
	ParseCloudfront   *cwtSourceOnly        `json:"parseCloudfront,omitempty"`
	ParseJSON         *cwtParseJSON         `json:"parseJSON,omitempty"`
	ParseKeyValue     *cwtParseKeyValue     `json:"parseKeyValue,omitempty"`
	ParseRoute53      *cwtSourceOnly        `json:"parseRoute53,omitempty"`
	ParseToOCSF       *cwtParseToOCSF       `json:"parseToOCSF,omitempty"`
	ParsePostgres     *cwtSourceOnly        `json:"parsePostgres,omitempty"`
	ParseVPC          *cwtSourceOnly        `json:"parseVPC,omitempty"`
	ParseWAF          *cwtSourceOnly        `json:"parseWAF,omitempty"`
	RenameKeys        *cwtRenameKeys        `json:"renameKeys,omitempty"`
	SplitString       *cwtSplitString       `json:"splitString,omitempty"`
	SubstituteString  *cwtSubstituteString  `json:"substituteString,omitempty"`
	TrimString        *cwtWithKeys          `json:"trimString,omitempty"`
	TypeConverter     *cwtTypeConverter     `json:"typeConverter,omitempty"`
	UpperCaseString   *cwtWithKeys          `json:"upperCaseString,omitempty"`
}

// The guide's transformer limits.
const (
	cwtMaxProcessors       = 20
	cwtMaxParsers          = 5
	cwtMaxNestedKeyDepth   = 3
	cwtMaxExtractedFields  = 200
	cwtMaxEventBytes       = 512 * 1024
	cwtMaxBackreferences   = 10
	cwtMaxDuplicateBackref = 2
)

// cwtConfigError carries the error code a refused transformer configuration
// answers with.
type cwtConfigError struct {
	Code    string
	Message string
}

func (e *cwtConfigError) Error() string { return e.Message }

func cwtInvalid(format string, args ...any) error {
	return &cwtConfigError{Code: "InvalidParameterException", Message: fmt.Sprintf(format, args...)}
}

type cwtStep interface {
	apply(ev *cwtEvent) error
}

// cwtEvent is one log event moving through a transformer: the raw message,
// the structured event the processors build, and the metadata copyValue reads.
type cwtEvent struct {
	message string
	root    *cwtObj
	meta    map[string]string
}

// source resolves a processor's source field; an empty source is @message.
func (ev *cwtEvent) source(path string) (any, bool) {
	if path == "" || path == "@message" {
		return ev.message, true
	}
	return ev.root.GetPath(path)
}

func (ev *cwtEvent) sourceString(path string) (string, error) {
	v, ok := ev.source(path)
	if !ok {
		return "", fmt.Errorf("source field %q is not present in the log event", cwtOr(path, "@message"))
	}
	s, isStr := v.(string)
	if !isStr {
		return "", fmt.Errorf("source field %q is not a string", path)
	}
	return s, nil
}

func cwtOr(p, def string) string {
	if p == "" {
		return def
	}
	return p
}

func cwtStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

type cwTransformer struct {
	steps []cwtStep
}

type cwtProcessorKind struct {
	name     string
	parser   bool
	vended   bool
	validate func(p cwtProcessor) (cwtStep, error)
}

func cwtKindOf(p cwtProcessor) []cwtProcessorKind {
	var kinds []cwtProcessorKind
	add := func(set bool, k cwtProcessorKind) {
		if set {
			kinds = append(kinds, k)
		}
	}
	add(p.AddKeys != nil, cwtProcessorKind{"addKeys", false, false, func(p cwtProcessor) (cwtStep, error) { return cwtCompileAddKeys(p.AddKeys) }})
	add(p.CopyValue != nil, cwtProcessorKind{"copyValue", false, false, func(p cwtProcessor) (cwtStep, error) { return cwtCompileCopyValue(p.CopyValue) }})
	add(p.CSV != nil, cwtProcessorKind{"csv", true, false, func(p cwtProcessor) (cwtStep, error) { return cwtCompileCSV(p.CSV) }})
	add(p.DateTimeConverter != nil, cwtProcessorKind{"dateTimeConverter", false, false, func(p cwtProcessor) (cwtStep, error) { return cwtCompileDateTime(p.DateTimeConverter) }})
	add(p.DeleteKeys != nil, cwtProcessorKind{"deleteKeys", false, false, func(p cwtProcessor) (cwtStep, error) { return cwtCompileDeleteKeys(p.DeleteKeys) }})
	add(p.Grok != nil, cwtProcessorKind{"grok", true, false, func(p cwtProcessor) (cwtStep, error) { return cwtCompileGrok(p.Grok) }})
	add(p.ListToMap != nil, cwtProcessorKind{"listToMap", false, false, func(p cwtProcessor) (cwtStep, error) { return cwtCompileListToMap(p.ListToMap) }})
	add(p.LowerCaseString != nil, cwtProcessorKind{"lowerCaseString", false, false, func(p cwtProcessor) (cwtStep, error) {
		return cwtCompileStringMutate("lowerCaseString", p.LowerCaseString, 10, strings.ToLower)
	}})
	add(p.MoveKeys != nil, cwtProcessorKind{"moveKeys", false, false, func(p cwtProcessor) (cwtStep, error) { return cwtCompileMoveKeys(p.MoveKeys) }})
	add(p.ParseCloudfront != nil, cwtProcessorKind{"parseCloudfront", true, true, func(p cwtProcessor) (cwtStep, error) {
		return cwtCompileVended("parseCloudfront", p.ParseCloudfront, cwtParseCloudfrontEvent)
	}})
	add(p.ParseJSON != nil, cwtProcessorKind{"parseJSON", true, false, func(p cwtProcessor) (cwtStep, error) { return cwtCompileParseJSON(p.ParseJSON) }})
	add(p.ParseKeyValue != nil, cwtProcessorKind{"parseKeyValue", true, false, func(p cwtProcessor) (cwtStep, error) { return cwtCompileParseKeyValue(p.ParseKeyValue) }})
	add(p.ParseRoute53 != nil, cwtProcessorKind{"parseRoute53", true, true, func(p cwtProcessor) (cwtStep, error) {
		return cwtCompileVended("parseRoute53", p.ParseRoute53, cwtParseRoute53Event)
	}})
	add(p.ParseToOCSF != nil, cwtProcessorKind{"parseToOCSF", true, true, func(p cwtProcessor) (cwtStep, error) { return cwtCompileParseToOCSF(p.ParseToOCSF) }})
	add(p.ParsePostgres != nil, cwtProcessorKind{"parsePostgres", true, true, func(p cwtProcessor) (cwtStep, error) {
		return cwtCompileVended("parsePostgres", p.ParsePostgres, cwtParsePostgresEvent)
	}})
	add(p.ParseVPC != nil, cwtProcessorKind{"parseVPC", true, true, func(p cwtProcessor) (cwtStep, error) {
		return cwtCompileVended("parseVPC", p.ParseVPC, cwtParseVPCEvent)
	}})
	add(p.ParseWAF != nil, cwtProcessorKind{"parseWAF", true, true, func(p cwtProcessor) (cwtStep, error) {
		return cwtCompileVended("parseWAF", p.ParseWAF, cwtParseWAFEvent)
	}})
	add(p.RenameKeys != nil, cwtProcessorKind{"renameKeys", false, false, func(p cwtProcessor) (cwtStep, error) { return cwtCompileRenameKeys(p.RenameKeys) }})
	add(p.SplitString != nil, cwtProcessorKind{"splitString", false, false, func(p cwtProcessor) (cwtStep, error) { return cwtCompileSplitString(p.SplitString) }})
	add(p.SubstituteString != nil, cwtProcessorKind{"substituteString", false, false, func(p cwtProcessor) (cwtStep, error) {
		return cwtCompileSubstituteString(p.SubstituteString)
	}})
	add(p.TrimString != nil, cwtProcessorKind{"trimString", false, false, func(p cwtProcessor) (cwtStep, error) {
		return cwtCompileStringMutate("trimString", p.TrimString, 10, strings.TrimSpace)
	}})
	add(p.TypeConverter != nil, cwtProcessorKind{"typeConverter", false, false, func(p cwtProcessor) (cwtStep, error) { return cwtCompileTypeConverter(p.TypeConverter) }})
	add(p.UpperCaseString != nil, cwtProcessorKind{"upperCaseString", false, false, func(p cwtProcessor) (cwtStep, error) {
		return cwtCompileStringMutate("upperCaseString", p.UpperCaseString, 10, strings.ToUpper)
	}})
	return kinds
}

// cwCompileTransformer checks a transformer configuration against the model's
// constraints and the guide's rules, and compiles it.
func cwCompileTransformer(procs []cwtProcessor) (*cwTransformer, error) {
	if len(procs) < 1 || len(procs) > cwtMaxProcessors {
		return nil, cwtInvalid("transformerConfig must contain between 1 and %d processors", cwtMaxProcessors)
	}
	t := &cwTransformer{}
	parsers, groks, addKeys, copyValues, vended := 0, 0, 0, 0, 0
	for i, p := range procs {
		kinds := cwtKindOf(p)
		if len(kinds) != 1 {
			return nil, cwtInvalid("Processor %d must specify exactly one processor type", i+1)
		}
		k := kinds[0]
		if i == 0 && !k.parser {
			return nil, cwtInvalid("The first processor in a transformer must be a parser, not %s", k.name)
		}
		if k.parser {
			parsers++
		}
		if k.vended {
			vended++
			if i != 0 {
				return nil, cwtInvalid("%s must be the first processor in the transformer", k.name)
			}
		}
		switch k.name {
		case "grok":
			groks++
		case "addKeys":
			addKeys++
		case "copyValue":
			copyValues++
		case "parseJSON":
			if i == 0 && cwtOr(cwtStr(p.ParseJSON.Source), "@message") != "@message" {
				return nil, cwtInvalid("parseJSON as the first processor must parse @message")
			}
		}
		step, err := k.validate(p)
		if err != nil {
			return nil, err
		}
		t.steps = append(t.steps, step)
	}
	switch {
	case parsers > cwtMaxParsers:
		return nil, cwtInvalid("A transformer can include at most %d parser-type processors", cwtMaxParsers)
	case vended > 1:
		return nil, cwtInvalid("A transformer can include only one parser for AWS vended logs")
	case groks > 1:
		return nil, cwtInvalid("A transformer can include only one grok processor")
	case addKeys > 1:
		return nil, cwtInvalid("A transformer can include only one addKeys processor")
	case copyValues > 1:
		return nil, cwtInvalid("A transformer can include only one copyValue processor")
	}
	return t, nil
}

// cwTransformResult is the outcome for one event: the transformed event's
// JSON, or why it could not be transformed.
type cwTransformResult struct {
	Transformed string
	Err         error
}

func (t *cwTransformer) Transform(message string, meta map[string]string) cwTransformResult {
	if len(message) >= cwtMaxEventBytes {
		return cwTransformResult{Err: fmt.Errorf("log event of %d bytes exceeds the %d-byte transformation limit", len(message), cwtMaxEventBytes)}
	}
	ev := &cwtEvent{message: message, root: newCWTObj(), meta: meta}
	for _, s := range t.steps {
		if err := s.apply(ev); err != nil {
			return cwTransformResult{Err: err}
		}
	}
	if n := cwtCountLeaves(ev.root); n > cwtMaxExtractedFields {
		return cwTransformResult{Err: fmt.Errorf("transformer extracted %d fields; the limit is %d", n, cwtMaxExtractedFields)}
	}
	return cwTransformResult{Transformed: cwtEncode(ev.root)}
}

func cwtCheckLen(field, v string, minLen, maxLen int) error {
	n := utf8.RuneCountInString(v)
	if n < minLen || (maxLen > 0 && n > maxLen) {
		if maxLen > 0 {
			return cwtInvalid("%s must be between %d and %d characters long", field, minLen, maxLen)
		}
		return cwtInvalid("%s must be at least %d characters long", field, minLen)
	}
	return nil
}

// cwtCheckKey enforces a key field's length and the guide's nested key depth.
func cwtCheckKey(field, v string, maxLen int) error {
	if err := cwtCheckLen(field, v, 1, maxLen); err != nil {
		return err
	}
	if strings.Count(v, ".")+1 > cwtMaxNestedKeyDepth {
		return cwtInvalid("%s %q exceeds the maximum nested key depth of %d", field, v, cwtMaxNestedKeyDepth)
	}
	return nil
}

func cwtCheckOptKey(field string, v *string) error {
	if v == nil {
		return nil
	}
	return cwtCheckKey(field, *v, 128)
}

func cwtCheckEntries(field string, n, maxN int) error {
	if n < 1 || n > maxN {
		return cwtInvalid("%s must contain between 1 and %d entries", field, maxN)
	}
	return nil
}

type cwtFunc func(ev *cwtEvent) error

func (f cwtFunc) apply(ev *cwtEvent) error { return f(ev) }

func cwtCompileParseJSON(c *cwtParseJSON) (cwtStep, error) {
	if c.Source != nil && *c.Source != "@message" {
		if err := cwtCheckKey("parseJSON.source", *c.Source, 128); err != nil {
			return nil, err
		}
	}
	if err := cwtCheckOptKey("parseJSON.destination", c.Destination); err != nil {
		return nil, err
	}
	src, dest := cwtStr(c.Source), cwtStr(c.Destination)
	return cwtFunc(func(ev *cwtEvent) error {
		raw, err := ev.sourceString(src)
		if err != nil {
			return err
		}
		v, err := cwtDecode(raw)
		if err != nil {
			return fmt.Errorf("parseJSON: %q is not valid JSON: %v", cwtOr(src, "@message"), err)
		}
		obj, isObj := v.(*cwtObj)
		if !isObj {
			return fmt.Errorf("parseJSON: %q is not a JSON object", cwtOr(src, "@message"))
		}
		if dest != "" {
			ev.root.SetPath(dest, obj, true)
			return nil
		}
		for _, k := range obj.keys {
			ev.root.Put(k, obj.m[k])
		}
		return nil
	}), nil
}

func cwtCompileCSV(c *cwtCSV) (cwtStep, error) {
	quote, delim := '"', ','
	if c.QuoteCharacter != nil {
		if err := cwtCheckLen("csv.quoteCharacter", *c.QuoteCharacter, 1, 1); err != nil {
			return nil, err
		}
		quote, _ = utf8.DecodeRuneInString(*c.QuoteCharacter)
	}
	if c.Delimiter != nil {
		switch d := *c.Delimiter; {
		case d == `\t`:
			delim = '\t'
		case d == `\s`:
			delim = ' '
		case utf8.RuneCountInString(d) == 1:
			delim, _ = utf8.DecodeRuneInString(d)
		default:
			return nil, cwtInvalid(`csv.delimiter must be a single character, \t or \s`)
		}
	}
	if len(c.Columns) > 100 {
		return nil, cwtInvalid("csv.columns can contain at most 100 columns")
	}
	for _, col := range c.Columns {
		if err := cwtCheckKey("csv.columns", col, 128); err != nil {
			return nil, err
		}
	}
	if c.Source != nil && *c.Source != "@message" {
		if err := cwtCheckKey("csv.source", *c.Source, 128); err != nil {
			return nil, err
		}
	}
	if err := cwtCheckOptKey("csv.destination", c.Destination); err != nil {
		return nil, err
	}
	src, dest, cols := cwtStr(c.Source), cwtStr(c.Destination), c.Columns
	return cwtFunc(func(ev *cwtEvent) error {
		raw, err := ev.sourceString(src)
		if err != nil {
			return err
		}
		values, err := cwtSplitCSV(raw, delim, quote)
		if err != nil {
			return fmt.Errorf("csv: %v", err)
		}
		out := ev.root
		if dest != "" {
			out = newCWTObj()
		}
		for i, v := range values {
			name := "column_" + strconv.Itoa(i+1)
			if i < len(cols) {
				name = cols[i]
			}
			out.SetPath(name, v, true)
		}
		if dest != "" {
			ev.root.SetPath(dest, out, true)
		}
		return nil
	}), nil
}

// A quoted column runs to its closing quote; a doubled quote inside it is a
// literal quote.
func cwtSplitCSV(s string, delim, quote rune) ([]string, error) {
	var out []string
	rs := []rune(s)
	i := 0
	for {
		var b strings.Builder
		if i < len(rs) && rs[i] == quote {
			i++
			closed := false
			for i < len(rs) {
				if rs[i] == quote {
					if i+1 < len(rs) && rs[i+1] == quote {
						b.WriteRune(quote)
						i += 2
						continue
					}
					i++
					closed = true
					break
				}
				b.WriteRune(rs[i])
				i++
			}
			if !closed {
				return nil, errors.New("unterminated quoted column")
			}
			for i < len(rs) && rs[i] != delim {
				b.WriteRune(rs[i])
				i++
			}
		} else {
			for i < len(rs) && rs[i] != delim {
				b.WriteRune(rs[i])
				i++
			}
		}
		out = append(out, b.String())
		if i >= len(rs) {
			return out, nil
		}
		i++
	}
}

func cwtCompileParseKeyValue(c *cwtParseKeyValue) (cwtStep, error) {
	if c.Source != nil && *c.Source != "@message" {
		if err := cwtCheckKey("parseKeyValue.source", *c.Source, 128); err != nil {
			return nil, err
		}
	}
	for field, v := range map[string]*string{
		"parseKeyValue.destination":       c.Destination,
		"parseKeyValue.fieldDelimiter":    c.FieldDelimiter,
		"parseKeyValue.keyValueDelimiter": c.KeyValueDelimiter,
		"parseKeyValue.keyPrefix":         c.KeyPrefix,
		"parseKeyValue.nonMatchValue":     c.NonMatchValue,
	} {
		if v != nil {
			if err := cwtCheckLen(field, *v, 1, 128); err != nil {
				return nil, err
			}
		}
	}
	src, dest := cwtStr(c.Source), cwtStr(c.Destination)
	fieldDelim, kvDelim := cwtOr(cwtStr(c.FieldDelimiter), "&"), cwtOr(cwtStr(c.KeyValueDelimiter), "=")
	prefix, overwrite, nonMatch := cwtStr(c.KeyPrefix), c.OverwriteIfExists, c.NonMatchValue
	return cwtFunc(func(ev *cwtEvent) error {
		raw, err := ev.sourceString(src)
		if err != nil {
			return err
		}
		out := ev.root
		if dest != "" {
			if existing, ok := ev.root.GetPath(dest); ok {
				obj, isObj := existing.(*cwtObj)
				if !isObj {
					return fmt.Errorf("parseKeyValue: destination %q is not an object", dest)
				}
				out = obj
			} else {
				out = newCWTObj()
				ev.root.SetPath(dest, out, true)
			}
		}
		for _, pair := range strings.Split(raw, fieldDelim) {
			if pair == "" {
				continue
			}
			k, v, found := strings.Cut(pair, kvDelim)
			var value any = v
			if !found {
				if nonMatch != nil {
					value = *nonMatch
				} else {
					value = nil
				}
			}
			if _, exists := out.Get(prefix + k); exists && !overwrite {
				continue
			}
			out.Put(prefix+k, value)
		}
		return nil
	}), nil
}

func cwtCompileStringMutate(name string, c *cwtWithKeys, maxKeys int, fn func(string) string) (cwtStep, error) {
	if err := cwtCheckEntries(name+".withKeys", len(c.WithKeys), maxKeys); err != nil {
		return nil, err
	}
	for _, k := range c.WithKeys {
		if err := cwtCheckLen(name+".withKeys", k, 1, 0); err != nil {
			return nil, err
		}
	}
	keys := c.WithKeys
	return cwtFunc(func(ev *cwtEvent) error {
		for _, k := range keys {
			if s, ok := cwtGetString(ev.root, k); ok {
				ev.root.SetPath(k, fn(s), true)
			}
		}
		return nil
	}), nil
}

func cwtGetString(o *cwtObj, path string) (string, bool) {
	v, ok := o.GetPath(path)
	if !ok {
		return "", false
	}
	s, isStr := v.(string)
	return s, isStr
}

func cwtCompileSplitString(c *cwtSplitString) (cwtStep, error) {
	if err := cwtCheckEntries("splitString.entries", len(c.Entries), 10); err != nil {
		return nil, err
	}
	for _, e := range c.Entries {
		if err := cwtCheckLen("splitString.source", e.Source, 1, 128); err != nil {
			return nil, err
		}
		if err := cwtCheckLen("splitString.delimiter", e.Delimiter, 1, 128); err != nil {
			return nil, err
		}
	}
	entries := c.Entries
	return cwtFunc(func(ev *cwtEvent) error {
		for _, e := range entries {
			s, ok := cwtGetString(ev.root, e.Source)
			if !ok {
				continue
			}
			parts := strings.Split(s, e.Delimiter)
			arr := make([]any, len(parts))
			for i, p := range parts {
				arr[i] = p
			}
			ev.root.SetPath(e.Source, arr, true)
		}
		return nil
	}), nil
}

type cwtSubstitution struct {
	source string
	re     *cwjRegex
	to     []cwtReplPart
}

func cwtCompileSubstituteString(c *cwtSubstituteString) (cwtStep, error) {
	if err := cwtCheckEntries("substituteString.entries", len(c.Entries), 10); err != nil {
		return nil, err
	}
	var subs []cwtSubstitution
	for _, e := range c.Entries {
		if err := cwtCheckKey("substituteString.source", e.Source, 128); err != nil {
			return nil, err
		}
		if err := cwtCheckLen("substituteString.from", e.From, 1, 128); err != nil {
			return nil, err
		}
		if err := cwtCheckLen("substituteString.to", e.To, 1, 128); err != nil {
			return nil, err
		}
		re, err := cwjCompile(e.From)
		if err != nil {
			return nil, cwtInvalid("substituteString.from %q is not a valid regular expression: %v", e.From, err)
		}
		to, err := cwtParseReplacement(e.To, re)
		if err != nil {
			return nil, err
		}
		subs = append(subs, cwtSubstitution{source: e.Source, re: re, to: to})
	}
	return cwtFunc(func(ev *cwtEvent) error {
		for _, s := range subs {
			v, ok := cwtGetString(ev.root, s.source)
			if !ok {
				continue
			}
			replaced, err := s.re.replaceAll(v, s.to)
			if err != nil {
				return fmt.Errorf("substituteString: %v", err)
			}
			ev.root.SetPath(s.source, replaced, true)
		}
		return nil
	}), nil
}

// cwtReplPart is one piece of a java.util.regex replacement string: literal
// text, or a reference to a numbered or named group.
type cwtReplPart struct {
	literal string
	group   int
	name    string
	isRef   bool
}

// cwtParseReplacement reads Matcher.appendReplacement syntax: $n takes as many
// digits as still name a group, ${name} names a group, and a backslash escapes
// the next character.
func cwtParseReplacement(to string, re *cwjRegex) ([]cwtReplPart, error) {
	var parts []cwtReplPart
	var lit strings.Builder
	refs := map[string]int{}
	flush := func() {
		if lit.Len() > 0 {
			parts = append(parts, cwtReplPart{literal: lit.String()})
			lit.Reset()
		}
	}
	groups := re.groups
	for i := 0; i < len(to); i++ {
		c := to[i]
		switch c {
		case '\\':
			i++
			if i >= len(to) {
				return nil, cwtInvalid("substituteString.to %q ends with a dangling backslash", to)
			}
			lit.WriteByte(to[i])
		case '$':
			i++
			if i >= len(to) {
				return nil, cwtInvalid("substituteString.to %q ends with a dangling $", to)
			}
			flush()
			if to[i] == '{' {
				end := strings.IndexByte(to[i:], '}')
				if end < 0 {
					return nil, cwtInvalid("substituteString.to %q has an unclosed group reference", to)
				}
				name := to[i+1 : i+end]
				if _, ok := re.group(name); !ok {
					return nil, cwtInvalid("substituteString.to references an unknown group %q", name)
				}
				parts = append(parts, cwtReplPart{name: name, isRef: true})
				refs["{"+name+"}"]++
				i += end
				continue
			}
			if to[i] < '0' || to[i] > '9' {
				return nil, cwtInvalid("substituteString.to %q has an illegal group reference", to)
			}
			n := int(to[i] - '0')
			for i+1 < len(to) && to[i+1] >= '0' && to[i+1] <= '9' {
				next := n*10 + int(to[i+1]-'0')
				if next > groups {
					break
				}
				n = next
				i++
			}
			if n > groups {
				return nil, cwtInvalid("substituteString.to references group %d, but the expression has %d groups", n, groups)
			}
			parts = append(parts, cwtReplPart{group: n, isRef: true})
			refs[strconv.Itoa(n)]++
		default:
			lit.WriteByte(c)
		}
	}
	flush()
	total := 0
	for _, n := range refs {
		total += n
		if n > cwtMaxDuplicateBackref {
			return nil, cwtInvalid("substituteString.to repeats a backreference more than %d times", cwtMaxDuplicateBackref)
		}
	}
	if total > cwtMaxBackreferences {
		return nil, cwtInvalid("substituteString.to contains more than %d backreferences", cwtMaxBackreferences)
	}
	return parts, nil
}

func cwtCompileAddKeys(c *cwtAddKeys) (cwtStep, error) {
	if err := cwtCheckEntries("addKeys.entries", len(c.Entries), 5); err != nil {
		return nil, err
	}
	for _, e := range c.Entries {
		if err := cwtCheckKey("addKeys.key", e.Key, 128); err != nil {
			return nil, err
		}
		if err := cwtCheckLen("addKeys.value", e.Value, 1, 256); err != nil {
			return nil, err
		}
	}
	entries := c.Entries
	return cwtFunc(func(ev *cwtEvent) error {
		for _, e := range entries {
			ev.root.SetPath(e.Key, e.Value, e.OverwriteIfExists)
		}
		return nil
	}), nil
}

func cwtCompileDeleteKeys(c *cwtWithKeys) (cwtStep, error) {
	if err := cwtCheckEntries("deleteKeys.withKeys", len(c.WithKeys), 5); err != nil {
		return nil, err
	}
	for _, k := range c.WithKeys {
		if err := cwtCheckLen("deleteKeys.withKeys", k, 1, 0); err != nil {
			return nil, err
		}
	}
	keys := c.WithKeys
	return cwtFunc(func(ev *cwtEvent) error {
		for _, k := range keys {
			ev.root.DeletePath(k)
		}
		return nil
	}), nil
}

// moveKeys moves a key into the target when the target is an object, as the
// guide's example moves outer_key1.inner_key1 into outer_key2, and otherwise
// moves it to the target path.
func cwtCompileMoveKeys(c *cwtMoveKeys) (cwtStep, error) {
	if err := cwtCheckEntries("moveKeys.entries", len(c.Entries), 5); err != nil {
		return nil, err
	}
	for _, e := range c.Entries {
		if err := cwtCheckKey("moveKeys.source", e.Source, 128); err != nil {
			return nil, err
		}
		if err := cwtCheckKey("moveKeys.target", e.Target, 128); err != nil {
			return nil, err
		}
	}
	entries := c.Entries
	return cwtFunc(func(ev *cwtEvent) error {
		for _, e := range entries {
			v, ok := ev.root.GetPath(e.Source)
			if !ok {
				continue
			}
			target := e.Target
			if t, exists := ev.root.GetPath(e.Target); exists {
				if _, isObj := t.(*cwtObj); isObj {
					parts := cwtSplitPath(e.Source)
					target = e.Target + "." + parts[len(parts)-1]
				}
			}
			if ev.root.SetPath(target, v, e.OverwriteIfExists) {
				ev.root.DeletePath(e.Source)
			}
		}
		return nil
	}), nil
}

func cwtCompileRenameKeys(c *cwtRenameKeys) (cwtStep, error) {
	if err := cwtCheckEntries("renameKeys.entries", len(c.Entries), 5); err != nil {
		return nil, err
	}
	for _, e := range c.Entries {
		if err := cwtCheckLen("renameKeys.key", e.Key, 1, 128); err != nil {
			return nil, err
		}
		if err := cwtCheckKey("renameKeys.renameTo", e.RenameTo, 128); err != nil {
			return nil, err
		}
	}
	entries := c.Entries
	return cwtFunc(func(ev *cwtEvent) error {
		for _, e := range entries {
			v, ok := ev.root.GetPath(e.Key)
			if !ok {
				continue
			}
			if ev.root.SetPath(e.RenameTo, v, e.OverwriteIfExists) {
				ev.root.DeletePath(e.Key)
			}
		}
		return nil
	}), nil
}

// The metadata keys copyValue can read.
var cwtMetadataKeys = map[string]bool{
	"@logGroupName": true, "@logGroupStream": true, "@accountId": true, "@regionName": true,
}

func cwtCompileCopyValue(c *cwtCopyValue) (cwtStep, error) {
	if err := cwtCheckEntries("copyValue.entries", len(c.Entries), 5); err != nil {
		return nil, err
	}
	for _, e := range c.Entries {
		if !cwtMetadataKeys[e.Source] {
			if err := cwtCheckKey("copyValue.source", e.Source, 128); err != nil {
				return nil, err
			}
		}
		if err := cwtCheckKey("copyValue.target", e.Target, 128); err != nil {
			return nil, err
		}
	}
	entries := c.Entries
	return cwtFunc(func(ev *cwtEvent) error {
		for _, e := range entries {
			var v any
			if cwtMetadataKeys[e.Source] {
				s, ok := ev.meta[e.Source]
				if !ok {
					continue
				}
				v = s
			} else {
				src, ok := ev.root.GetPath(e.Source)
				if !ok {
					continue
				}
				v = cwtDeepCopy(src)
			}
			ev.root.SetPath(e.Target, v, e.OverwriteIfExists)
		}
		return nil
	}), nil
}

func cwtCompileListToMap(c *cwtListToMap) (cwtStep, error) {
	if err := cwtCheckKey("listToMap.source", c.Source, 128); err != nil {
		return nil, err
	}
	if err := cwtCheckLen("listToMap.key", c.Key, 1, 128); err != nil {
		return nil, err
	}
	if c.ValueKey != nil {
		if err := cwtCheckLen("listToMap.valueKey", *c.ValueKey, 1, 128); err != nil {
			return nil, err
		}
	}
	if err := cwtCheckOptKey("listToMap.target", c.Target); err != nil {
		return nil, err
	}
	last := false
	if c.FlattenedElement != nil {
		switch *c.FlattenedElement {
		case "first":
		case "last":
			last = true
		default:
			return nil, cwtInvalid("listToMap.flattenedElement must be first or last")
		}
	}
	if c.Flatten && c.FlattenedElement == nil {
		return nil, cwtInvalid("listToMap.flattenedElement is required when flatten is true")
	}
	cfg := *c
	return cwtFunc(func(ev *cwtEvent) error {
		v, ok := ev.root.GetPath(cfg.Source)
		if !ok {
			return nil
		}
		list, isList := v.([]any)
		if !isList {
			return nil
		}
		out := newCWTObj()
		for _, item := range list {
			obj, isObj := item.(*cwtObj)
			if !isObj {
				continue
			}
			kv, ok := obj.Get(cfg.Key)
			if !ok {
				continue
			}
			key, isScalar := cwtScalarString(kv)
			if !isScalar {
				continue
			}
			val := cwtDeepCopy(obj)
			if cfg.ValueKey != nil {
				vv, ok := obj.Get(*cfg.ValueKey)
				if !ok {
					continue
				}
				val = cwtDeepCopy(vv)
			}
			existing, seen := out.Get(key)
			switch {
			case !cfg.Flatten && seen:
				arr, _ := existing.([]any)
				out.Put(key, append(arr, val))
			case !cfg.Flatten:
				out.Put(key, []any{val})
			case !seen || last:
				out.Put(key, val)
			}
		}
		if cfg.Target != nil {
			ev.root.SetPath(*cfg.Target, out, true)
			return nil
		}
		for _, k := range out.keys {
			ev.root.Put(k, out.m[k])
		}
		return nil
	}), nil
}

func cwtCompileTypeConverter(c *cwtTypeConverter) (cwtStep, error) {
	if err := cwtCheckEntries("typeConverter.entries", len(c.Entries), 5); err != nil {
		return nil, err
	}
	for _, e := range c.Entries {
		if err := cwtCheckKey("typeConverter.key", e.Key, 128); err != nil {
			return nil, err
		}
		switch e.Type {
		case "integer", "double", "string", "boolean":
		default:
			return nil, cwtInvalid("typeConverter.type must be one of integer, double, string, boolean")
		}
	}
	entries := c.Entries
	return cwtFunc(func(ev *cwtEvent) error {
		for _, e := range entries {
			v, ok := ev.root.GetPath(e.Key)
			if !ok {
				continue
			}
			converted, err := cwtConvertType(v, e.Type)
			if err != nil {
				return fmt.Errorf("typeConverter: %s: %v", e.Key, err)
			}
			ev.root.SetPath(e.Key, converted, true)
		}
		return nil
	}), nil
}

func cwtConvertType(v any, typ string) (any, error) {
	s, isScalar := cwtScalarString(v)
	if !isScalar {
		return nil, fmt.Errorf("value is not a scalar")
	}
	switch typ {
	case "string":
		return s, nil
	case "boolean":
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "true":
			return true, nil
		case "false":
			return false, nil
		}
		return nil, fmt.Errorf("%q is not a boolean", s)
	case "integer":
		n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		if err != nil {
			if f, ferr := strconv.ParseFloat(strings.TrimSpace(s), 64); ferr == nil && f == math.Trunc(f) && math.Abs(f) < 1<<63 {
				if _, isNum := v.(json.Number); isNum {
					return json.Number(strconv.FormatInt(int64(f), 10)), nil
				}
			}
			return nil, fmt.Errorf("%q is not an integer", s)
		}
		return json.Number(strconv.FormatInt(n, 10)), nil
	case "double":
		f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
			return nil, fmt.Errorf("%q is not a double", s)
		}
		return json.Number(cwtJavaDouble(f)), nil
	}
	return nil, fmt.Errorf("unknown type %q", typ)
}

// cwtJavaDouble prints a double the way Java's Double.toString does for the
// plain-notation range: an integral value keeps a ".0".
func cwtJavaDouble(f float64) string {
	abs := math.Abs(f)
	if abs != 0 && (abs < 1e-3 || abs >= 1e7) {
		mant := strconv.FormatFloat(f, 'E', -1, 64)
		m, e, _ := strings.Cut(mant, "E")
		if !strings.Contains(m, ".") {
			m += ".0"
		}
		exp, _ := strconv.Atoi(e)
		return m + "E" + strconv.Itoa(exp)
	}
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

var cwtMappingVersionRE = regexp.MustCompile(`^\d+\.\d+(\.\d+)?$`)

// cwGroupTransformer is the transformer that applies to group: its own, or
// else the account-level transformer policy whose selection admits it, with
// the metric dimensions each publishes under.
func cwGroupTransformer(group string) ([]cwtProcessor, []CWDimension, bool) {
	if cfg, ok := cwTransformers.Get(group); ok {
		return cfg.TransformerConfig, []CWDimension{{Name: "LogGroupname", Value: group}}, true
	}
	for _, p := range cwAccountPoliciesOf(cwPolicyTransformer) {
		if !cwPolicySelects(p.PolicyType, p.SelectionCriteria, group) {
			continue
		}
		procs, err := cwParseTransformerPolicyDocument(p.PolicyDocument)
		if err != nil {
			log.Printf("cloudwatch logs: stored transformer policy %s no longer compiles: %v", p.PolicyName, err)
			return nil, nil, false
		}
		return procs, []CWDimension{{Name: "PolicyLevel", Value: cwPolicyLevelAccountDimension}}, true
	}
	return nil, nil, false
}

// cwTransformIngested runs a log group's transformer over events as they are
// ingested, recording each event's transformed version or its transformation
// error beside the original, and publishes the AWS/Logs transformer metrics.
func cwTransformIngested(group, stream string, events []CWLogEvent) {
	procs, dims, ok := cwGroupTransformer(group)
	if !ok {
		return
	}
	t, err := cwCompileTransformer(procs)
	if err != nil {
		log.Printf("cloudwatch logs: stored transformer of %s no longer compiles: %v", group, err)
		return
	}
	meta := map[string]string{
		"@logGroupName":   group,
		"@logGroupStream": stream,
		"@accountId":      awsAccountID(),
		"@regionName":     awsRegion(),
	}
	transformed, failed, bytes := 0, 0, 0
	for i := range events {
		res := t.Transform(events[i].Message, meta)
		if res.Err != nil {
			events[i].TransformationError = res.Err.Error()
			failed++
			continue
		}
		events[i].Transformed = res.Transformed
		transformed++
		bytes += len(res.Transformed)
	}
	now := float64(time.Now().Unix())
	if transformed > 0 {
		cwStoreDatum(CWMetricDatum{Namespace: "AWS/Logs", MetricName: "TransformedLogEvents", Dimensions: dims, Value: float64(transformed), Timestamp: now, Unit: "None"})
		cwStoreDatum(CWMetricDatum{Namespace: "AWS/Logs", MetricName: "TransformedBytes", Dimensions: dims, Value: float64(bytes), Timestamp: now, Unit: "Bytes"})
	}
	if failed > 0 {
		cwStoreDatum(CWMetricDatum{Namespace: "AWS/Logs", MetricName: "TransformationErrors", Dimensions: dims, Value: float64(failed), Timestamp: now, Unit: "None"})
	}
}
