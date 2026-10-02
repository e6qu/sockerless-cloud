package main

import (
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type kqlTokenKind int

const (
	kqlEOF kqlTokenKind = iota
	kqlIdent
	kqlString
	kqlLong
	kqlReal
	kqlTimespan
	kqlDatetime
	kqlPunct
)

type kqlToken struct {
	kind kqlTokenKind
	text string // the identifier, the punctuation, or the source spelling of a literal
	str  string // a string literal's decoded value, or a datetime literal's content
	num  int64
	real float64
	span time.Duration
	pos  int
}

// kqlHyphenatedKeywords are the operator names Kusto spells with a hyphen,
// which the lexer joins so that `project-away` is one word and `a-b` stays a
// subtraction.
var kqlHyphenatedKeywords = map[string]bool{
	"project-away": true, "project-keep": true, "project-rename": true, "project-reorder": true,
	"mv-expand": true, "mv-apply": true, "make-series": true, "top-nested": true,
	"top-hitters": true, "parse-where": true, "parse-kv": true, "find-in": true,
}

// kqlNegatableOperators are the string operators Kusto negates with a leading
// `!`, as in `!contains`.
var kqlNegatableOperators = map[string]bool{
	"contains": true, "contains_cs": true, "has": true, "has_cs": true,
	"startswith": true, "startswith_cs": true, "endswith": true, "endswith_cs": true,
	"in": true, "between": true, "hasprefix": true, "hassuffix": true,
}

var kqlTimespanUnits = map[string]time.Duration{
	"d": 24 * time.Hour, "day": 24 * time.Hour, "days": 24 * time.Hour,
	"h": time.Hour, "hr": time.Hour, "hrs": time.Hour, "hour": time.Hour, "hours": time.Hour,
	"m": time.Minute, "min": time.Minute, "minute": time.Minute, "minutes": time.Minute,
	"s": time.Second, "sec": time.Second, "second": time.Second, "seconds": time.Second,
	"ms": time.Millisecond, "milli": time.Millisecond, "millis": time.Millisecond,
	"millisecond": time.Millisecond, "milliseconds": time.Millisecond,
	"microsecond": time.Microsecond, "microseconds": time.Microsecond,
	"tick": 100 * time.Nanosecond, "ticks": 100 * time.Nanosecond,
}

func kqlIsIdentStart(r rune) bool { return r == '_' || r == '$' || unicode.IsLetter(r) }
func kqlIsIdentPart(r rune) bool  { return kqlIsIdentStart(r) || unicode.IsDigit(r) }

// lexKQL splits a query into tokens. A string literal is one token whatever it
// holds, so a `|` inside quotes never separates two operators.
func lexKQL(src string) ([]kqlToken, *kqlError) {
	var toks []kqlToken
	i := 0
	for i < len(src) {
		r, size := utf8.DecodeRuneInString(src[i:])
		switch {
		case unicode.IsSpace(r):
			i += size
			continue
		case strings.HasPrefix(src[i:], "//"):
			for i < len(src) && src[i] != '\n' {
				i++
			}
			continue
		}
		start := i

		if (r == 'h' || r == 'H') && strings.HasPrefix(src[i+1:], "@") {
			// h@"..." is an obfuscated verbatim literal.
			if j := i + 2; j < len(src) && (src[j] == '"' || src[j] == '\'') {
				value, end, err := lexKQLString(src, j, true)
				if err != nil {
					return nil, err
				}
				toks = append(toks, kqlToken{kind: kqlString, text: src[start:end], str: value, pos: start})
				i = end
				continue
			}
		}
		if (r == '@' || r == 'h' || r == 'H') && i+1 < len(src) && (src[i+1] == '"' || src[i+1] == '\'') {
			verbatim := r == '@'
			value, end, err := lexKQLString(src, i+1, verbatim)
			if err != nil {
				return nil, err
			}
			toks = append(toks, kqlToken{kind: kqlString, text: src[start:end], str: value, pos: start})
			i = end
			continue
		}
		if r == '"' || r == '\'' {
			value, end, err := lexKQLString(src, i, false)
			if err != nil {
				return nil, err
			}
			toks = append(toks, kqlToken{kind: kqlString, text: src[start:end], str: value, pos: start})
			i = end
			continue
		}

		if unicode.IsDigit(r) {
			tok, end, err := lexKQLNumber(src, i)
			if err != nil {
				return nil, err
			}
			toks = append(toks, tok)
			i = end
			continue
		}

		if kqlIsIdentStart(r) {
			end := i
			for end < len(src) {
				c, n := utf8.DecodeRuneInString(src[end:])
				if !kqlIsIdentPart(c) {
					break
				}
				end += n
			}
			word := src[i:end]
			if end < len(src) && src[end] == '-' {
				rest := end + 1
				for rest < len(src) && (src[rest] == '_' || unicode.IsLetter(rune(src[rest]))) {
					rest++
				}
				if kqlHyphenatedKeywords[src[i:rest]] {
					word, end = src[i:rest], rest
				}
			}
			if word == "in" && end < len(src) && src[end] == '~' {
				word, end = src[i:end+1], end+1
			}
			if word == "datetime" {
				j := end
				for j < len(src) && (src[j] == ' ' || src[j] == '\t') {
					j++
				}
				if j < len(src) && src[j] == '(' {
					closing := strings.IndexByte(src[j:], ')')
					if closing < 0 {
						return nil, kqlSyntaxError(src, len(src), "")
					}
					content := strings.TrimSpace(src[j+1 : j+closing])
					content = strings.Trim(content, `"'`)
					toks = append(toks, kqlToken{kind: kqlDatetime, text: src[i : j+closing+1], str: content, pos: start})
					i = j + closing + 1
					continue
				}
			}
			toks = append(toks, kqlToken{kind: kqlIdent, text: word, pos: start})
			i = end
			continue
		}

		if r == '!' {
			end := i + 1
			for end < len(src) && kqlIsIdentPart(rune(src[end])) {
				end++
			}
			word := src[i+1 : end]
			if kqlNegatableOperators[word] {
				if word == "in" && end < len(src) && src[end] == '~' {
					end++
				}
				toks = append(toks, kqlToken{kind: kqlIdent, text: src[i:end], pos: start})
				i = end
				continue
			}
		}

		punct := ""
		for _, p := range []string{"==", "!=", "<=", ">=", "=~", "!~", ".."} {
			if strings.HasPrefix(src[i:], p) {
				punct = p
				break
			}
		}
		if punct == "" && strings.ContainsRune("|,()=<>+-*/%.;[]", r) {
			punct = string(r)
		}
		if punct == "" {
			return nil, kqlSyntaxError(src, start, string(r))
		}
		toks = append(toks, kqlToken{kind: kqlPunct, text: punct, pos: start})
		i += len(punct)
	}
	toks = append(toks, kqlToken{kind: kqlEOF, pos: len(src)})
	return toks, nil
}

// lexKQLString reads the quoted literal whose opening quote is at src[i]. A
// verbatim literal (@"...") takes every character as written but a doubled
// quote; a regular one honours Kusto's backslash escapes.
func lexKQLString(src string, i int, verbatim bool) (string, int, *kqlError) {
	quote := src[i]
	var b strings.Builder
	j := i + 1
	for j < len(src) {
		c := src[j]
		if c == quote {
			// A verbatim literal spells its own quote character twice.
			if verbatim && j+1 < len(src) && src[j+1] == quote {
				b.WriteByte(quote)
				j += 2
				continue
			}
			return b.String(), j + 1, nil
		}
		if c == '\n' && !verbatim {
			break
		}
		if c == '\\' && !verbatim {
			if j+1 >= len(src) {
				break
			}
			e := src[j+1]
			switch e {
			case '\\', '"', '\'':
				b.WriteByte(e)
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case '0':
				b.WriteByte(0)
			case 'u':
				if j+6 > len(src) {
					return "", 0, kqlSyntaxError(src, j, src[j:])
				}
				code, err := strconv.ParseUint(src[j+2:j+6], 16, 32)
				if err != nil {
					return "", 0, kqlSyntaxError(src, j, src[j:j+6])
				}
				b.WriteRune(rune(code))
				j += 6
				continue
			default:
				return "", 0, kqlSyntaxError(src, j, src[j:j+2])
			}
			j += 2
			continue
		}
		b.WriteByte(c)
		j++
	}
	return "", 0, kqlSyntaxError(src, i, src[i:j])
}

// lexKQLNumber reads a long, a real, or — when a unit follows the digits with
// no space — a timespan such as 5m or 1.5h.
func lexKQLNumber(src string, i int) (kqlToken, int, *kqlError) {
	j := i
	isReal := false
	for j < len(src) && src[j] >= '0' && src[j] <= '9' {
		j++
	}
	if j+1 < len(src) && src[j] == '.' && src[j+1] >= '0' && src[j+1] <= '9' {
		isReal = true
		j++
		for j < len(src) && src[j] >= '0' && src[j] <= '9' {
			j++
		}
	}
	if j < len(src) && (src[j] == 'e' || src[j] == 'E') {
		k := j + 1
		if k < len(src) && (src[k] == '+' || src[k] == '-') {
			k++
		}
		if k < len(src) && src[k] >= '0' && src[k] <= '9' {
			isReal = true
			j = k
			for j < len(src) && src[j] >= '0' && src[j] <= '9' {
				j++
			}
		}
	}
	digits := src[i:j]
	u := j
	for u < len(src) && unicode.IsLetter(rune(src[u])) {
		u++
	}
	if u > j {
		unit, ok := kqlTimespanUnits[src[j:u]]
		if !ok {
			return kqlToken{}, 0, kqlSyntaxError(src, i, src[i:u])
		}
		f, err := strconv.ParseFloat(digits, 64)
		if err != nil {
			return kqlToken{}, 0, kqlSyntaxError(src, i, src[i:u])
		}
		return kqlToken{kind: kqlTimespan, text: src[i:u], span: time.Duration(f * float64(unit)), pos: i}, u, nil
	}
	if isReal {
		f, err := strconv.ParseFloat(digits, 64)
		if err != nil {
			return kqlToken{}, 0, kqlSyntaxError(src, i, digits)
		}
		return kqlToken{kind: kqlReal, text: digits, real: f, pos: i}, j, nil
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return kqlToken{}, 0, kqlSyntaxError(src, i, digits)
	}
	return kqlToken{kind: kqlLong, text: digits, num: n, pos: i}, j, nil
}
