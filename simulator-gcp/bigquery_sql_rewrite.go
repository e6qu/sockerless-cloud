package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// bqHiddenColumnPrefix names the columns the QUALIFY rewrite adds to carry
// window values into the filtering query; the result leaves them out.
const bqHiddenColumnPrefix = "bq_qualify_"

// bqReservedWords are never column references.
var bqReservedWords = map[string]bool{
	"SELECT": true, "FROM": true, "WHERE": true, "AND": true, "OR": true, "NOT": true, "NULL": true, "TRUE": true, "FALSE": true,
	"AS": true, "ON": true, "JOIN": true, "CASE": true, "WHEN": true, "THEN": true, "ELSE": true, "END": true, "IN": true, "IS": true,
	"LIKE": true, "BETWEEN": true, "GROUP": true, "BY": true, "ORDER": true, "HAVING": true, "LIMIT": true, "OFFSET": true,
	"DISTINCT": true, "ASC": true, "DESC": true, "UNION": true, "ALL": true, "EXISTS": true, "OVER": true, "PARTITION": true,
	"ROWS": true, "RANGE": true, "UNBOUNDED": true, "PRECEDING": true, "FOLLOWING": true, "CURRENT": true, "ROW": true,
	"INTERVAL": true, "NULLS": true, "FIRST": true, "LAST": true, "WITH": true, "RECURSIVE": true, "QUALIFY": true, "WINDOW": true,
	"EXCEPT": true, "REPLACE": true, "STRUCT": true, "ARRAY": true, "UNNEST": true, "INNER": true, "LEFT": true, "RIGHT": true,
	"FULL": true, "CROSS": true, "OUTER": true, "USING": true, "INTERSECT": true, "SET": true, "VALUES": true, "INTO": true,
	"INSERT": true, "UPDATE": true, "DELETE": true, "MERGE": true, "MATCHED": true, "TARGET": true, "SOURCE": true, "ESCAPE": true,
	"IGNORE": true, "RESPECT": true, "GROUPS": true, "FILTER": true, "COLLATE": true, "CAST": true, "SAFE_CAST": true, "EXTRACT": true,
	"IF": true, "TABLESAMPLE": true, "NATURAL": true, "DEFAULT": true, "ANY": true, "SOME": true, "LATERAL": true, "FETCH": true,
	"AT": true, "ZONE": true, "TIME": true, "DATE": true, "TIMESTAMP": true, "DATETIME": true, "LOOKUP": true, "ASSERT_ROWS_MODIFIED": true,
	"CONTAINS": true, "CUBE": true, "ENUM": true, "GROUPING": true, "HASH": true, "NEW": true, "NO": true, "OF": true, "PROTO": true,
	"ROLLUP": true, "TO": true, "TREAT": true, "WITHIN": true, "FOR": true, "CREATE": true, "DEFINE": true, "ESCAPE_": true,
}

// bqReservedFunctions are keywords that are also function names when a
// parenthesis follows them.
var bqReservedFunctions = map[string]bool{"DATE": true, "DATETIME": true, "TIME": true, "TIMESTAMP": true, "IF": true}

var bqDateParts = map[string]bool{
	"MICROSECOND": true, "MILLISECOND": true, "SECOND": true, "MINUTE": true, "HOUR": true, "DAY": true, "WEEK": true, "ISOWEEK": true,
	"MONTH": true, "QUARTER": true, "YEAR": true, "ISOYEAR": true, "DAYOFWEEK": true, "DAYOFYEAR": true, "DATE": true, "DATETIME": true, "TIME": true,
}

// bqDatePartFunctions take a bare date part name among their arguments.
var bqDatePartFunctions = map[string]bool{
	"TIMESTAMP_TRUNC": true, "DATE_TRUNC": true, "DATETIME_TRUNC": true, "TIME_TRUNC": true,
	"TIMESTAMP_DIFF": true, "DATE_DIFF": true, "DATETIME_DIFF": true, "TIME_DIFF": true, "LAST_DAY": true,
}

// bqRewriteExpressions rewrites the GoogleSQL expressions of tokens onto
// what SQLite runs: literals, parameters, CURRENT_* values, typed literals,
// intervals, EXTRACT, CAST, the SAFE. prefix, array and struct literals,
// subscripts, UNNEST in IN, field access into records and UNNEST range
// variables, and the registered functions' names.
func bqRewriteExpressions(tokens []bqSQLToken, scope *bqScope) ([]bqSQLToken, error) {
	var out []bqSQLToken
	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		if tok.fixed || tok.isSpace() || tok.kind == bqTokString || tok.kind == bqTokNumber {
			out = append(out, tok)
			continue
		}
		if tok.kind == bqTokPunct {
			switch tok.text {
			case "@":
				next := bqSignificant(tokens, i+1)
				if next < len(tokens) && tokens[next].isIdent() {
					lit, ok := scope.parameters[strings.ToLower(tokens[next].text)]
					if !ok {
						return nil, bqQueryErr("Query parameter '%s' not found", tokens[next].text)
					}
					out = append(out, lit...)
					i = next
					continue
				}
			case "?":
				if scope.nextParam >= len(scope.positional) {
					return nil, bqQueryErr("Query has %d positional parameters but only %d were given", scope.nextParam+1, len(scope.positional))
				}
				out = append(out, scope.positional[scope.nextParam]...)
				scope.nextParam++
				continue
			case "(":
				end := bqMatchParen(tokens, i)
				if end < 0 {
					return nil, bqQueryErr("Syntax error: Unmatched (")
				}
				inner, err := bqRewriteExpressions(tokens[i+1:end], scope)
				if err != nil {
					return nil, err
				}
				out = append(out, bqPunctToken("("))
				out = append(out, inner...)
				out = append(out, bqPunctToken(")"))
				i = end
				continue
			case "[":
				end := bqMatchParen(tokens, i)
				if end < 0 {
					return nil, bqQueryErr("Syntax error: Unmatched [")
				}
				inner, err := bqRewriteExpressions(tokens[i+1:end], scope)
				if err != nil {
					return nil, err
				}
				prev := bqPrevSignificant(out, len(out))
				if prev >= 0 && (out[prev].isIdent() || out[prev].isPunct(")") || out[prev].isPunct("]") || out[prev].kind == bqTokString) && !bqIsOperatorWord(out[prev]) {
					var err error
					out, err = bqRewriteSubscript(out, inner)
					if err != nil {
						return nil, err
					}
				} else {
					out = append(out, bqWordToken("json_array"), bqPunctToken("("))
					out = append(out, inner...)
					out = append(out, bqPunctToken(")"))
				}
				i = end
				continue
			case "/":
				// GoogleSQL divides INT64 operands into a FLOAT64; SQLite
				// would truncate, so the dividend becomes REAL first.
				out = append(out, bqPunctToken("/"), bqSpaceToken(), bqSQLToken{text: "1.0", kind: bqTokNumber, fixed: true}, bqSpaceToken(), bqPunctToken("/"))
				continue
			}
			out = append(out, tok)
			continue
		}
		if tok.kind == bqTokQuoted {
			// A backtick identifier: a column path or an alias.
			path, end := bqReadPath(tokens, i)
			replacement, err := bqRewritePath(path, out, scope)
			if err != nil {
				return nil, err
			}
			out = append(out, replacement...)
			i = end
			continue
		}
		upper := tok.upper()
		if prev := bqPrevSignificant(out, len(out)); prev >= 0 && out[prev].isWord("AS") && !bqReservedWords[upper] {
			out = append(out, bqQuotedToken(tok.text))
			continue
		}
		next := bqSignificant(tokens, i+1)
		followedByParen := next < len(tokens) && tokens[next].isPunct("(")
		switch {
		case upper == "CURRENT_TIMESTAMP", upper == "CURRENT_DATE", upper == "CURRENT_DATETIME", upper == "CURRENT_TIME":
			var args []bqSQLToken
			end := i
			if followedByParen {
				end = bqMatchParen(tokens, next)
				var err error
				if args, err = bqRewriteExpressions(tokens[next+1:end], scope); err != nil {
					return nil, err
				}
			}
			out = append(out, bqCurrentTokens(upper, args, scope)...)
			i = end
			continue
		case (upper == "TIMESTAMP" || upper == "DATE" || upper == "DATETIME" || upper == "TIME" || upper == "NUMERIC" || upper == "BIGNUMERIC" || upper == "JSON") &&
			next < len(tokens) && tokens[next].kind == bqTokString:
			lit := tokens[next]
			switch upper {
			case "NUMERIC", "BIGNUMERIC", "JSON":
				out = append(out, bqWordToken("bq_cast"), bqPunctToken("("), lit, bqPunctToken(","), bqStringToken(upper), bqPunctToken(","), bqStringToken(upper), bqPunctToken(")"))
			default:
				out = append(out, bqWordToken(bqSQLiteName(upper)), bqPunctToken("("), lit, bqPunctToken(")"))
			}
			i = next
			continue
		case upper == "INTERVAL":
			exprTokens, unit, end, err := bqReadInterval(tokens, i)
			if err != nil {
				return nil, err
			}
			inner, err := bqRewriteExpressions(exprTokens, scope)
			if err != nil {
				return nil, err
			}
			out = append(out, inner...)
			out = append(out, bqPunctToken(","), bqSpaceToken(), bqStringToken(unit))
			i = end
			continue
		case upper == "EXTRACT" && followedByParen:
			end := bqMatchParen(tokens, next)
			replacement, err := bqRewriteExtract(tokens[next+1:end], scope)
			if err != nil {
				return nil, err
			}
			out = append(out, replacement...)
			i = end
			continue
		case (upper == "CAST" || upper == "SAFE_CAST") && followedByParen:
			end := bqMatchParen(tokens, next)
			replacement, err := bqRewriteCast(upper, tokens[next+1:end], scope)
			if err != nil {
				return nil, err
			}
			out = append(out, replacement...)
			i = end
			continue
		case upper == "ARRAY" && next < len(tokens) && (tokens[next].isPunct("<") || tokens[next].isPunct("[")):
			// A typed or ARRAY-prefixed array literal: the elements say it all.
			if tokens[next].isPunct("<") {
				close := bqMatchAngle(tokens, next)
				if close < 0 {
					return nil, bqQueryErr("Syntax error: Unmatched <")
				}
				i = close
			} else {
				i = next - 1
			}
			continue
		case upper == "STRUCT" && next < len(tokens) && tokens[next].isPunct("<"):
			close := bqMatchAngle(tokens, next)
			if close < 0 {
				return nil, bqQueryErr("Syntax error: Unmatched <")
			}
			out = append(out, tok)
			i = close
			continue
		case upper == "STRUCT" && followedByParen:
			end := bqMatchParen(tokens, next)
			replacement, err := bqRewriteStruct(tokens[next+1:end], scope)
			if err != nil {
				return nil, err
			}
			out = append(out, replacement...)
			i = end
			continue
		case upper == "UNNEST" && followedByParen:
			end := bqMatchParen(tokens, next)
			inner, err := bqRewriteExpressions(tokens[next+1:end], scope)
			if err != nil {
				return nil, err
			}
			out = append(out, bqPunctToken("("), bqWordToken("SELECT"), bqSpaceToken(), bqWordToken("value"), bqSpaceToken(), bqWordToken("FROM"), bqSpaceToken(), bqWordToken("json_each"), bqPunctToken("("))
			out = append(out, inner...)
			out = append(out, bqPunctToken(")"), bqPunctToken(")"))
			i = end
			continue
		case upper == "SAFE" && next < len(tokens) && tokens[next].isPunct("."):
			name := bqSignificant(tokens, next+1)
			open := bqSignificant(tokens, name+1)
			if name >= len(tokens) || !tokens[name].isIdent() || open >= len(tokens) || !tokens[open].isPunct("(") {
				return nil, bqQueryErr("Syntax error: Expected a function call after SAFE.")
			}
			end := bqMatchParen(tokens, open)
			replacement, err := bqRewriteCall("SAFE_"+strings.ToUpper(tokens[name].text), tokens[open+1:end], scope)
			if err != nil {
				return nil, err
			}
			out = append(out, replacement...)
			i = end
			continue
		case followedByParen && (!bqReservedWords[upper] || bqReservedFunctions[upper]) && !bqAfterDot(out):
			end := bqMatchParen(tokens, next)
			if end < 0 {
				return nil, bqQueryErr("Syntax error: Unmatched (")
			}
			replacement, err := bqRewriteCall(upper, tokens[next+1:end], scope)
			if err != nil {
				return nil, err
			}
			out = append(out, replacement...)
			i = end
			continue
		case !bqReservedWords[upper]:
			path, end := bqReadPath(tokens, i)
			replacement, err := bqRewritePath(path, out, scope)
			if err != nil {
				return nil, err
			}
			out = append(out, replacement...)
			i = end
			continue
		}
		out = append(out, tok)
	}
	return out, nil
}

// bqAfterDot reports whether the last token written is a `.`, which makes
// the next identifier a field name rather than a function.
func bqAfterDot(out []bqSQLToken) bool {
	prev := bqPrevSignificant(out, len(out))
	return prev >= 0 && out[prev].isPunct(".")
}

func bqIsOperatorWord(t bqSQLToken) bool {
	switch t.upper() {
	case "AND", "OR", "NOT", "IN", "IS", "LIKE", "BETWEEN", "THEN", "ELSE", "WHEN", "SELECT", "WHERE", "ON", "BY", "AS", "FROM", "HAVING", "SET", "VALUES", "RETURNING":
		return true
	}
	return false
}

// bqMatchAngle is the index of the `>` closing the `<` at open in a type.
func bqMatchAngle(tokens []bqSQLToken, open int) int {
	depth := 0
	for i := open; i < len(tokens); i++ {
		switch {
		case tokens[i].isPunct("<"):
			depth++
		case tokens[i].isPunct(">"):
			depth--
			if depth == 0 {
				return i
			}
		case tokens[i].isPunct(">>"):
			depth -= 2
			if depth <= 0 {
				return i
			}
		}
	}
	return -1
}

// bqReadPath reads an identifier path starting at i and reports its end.
func bqReadPath(tokens []bqSQLToken, i int) ([]string, int) {
	path := []string{tokens[i].text}
	for i+2 < len(tokens) && tokens[i+1].isPunct(".") && tokens[i+2].isIdent() && !tokens[i+2].fixed {
		path = append(path, tokens[i+2].text)
		i += 2
	}
	return path, i
}

// bqRewritePath resolves a column path against the scope: an UNNEST range
// variable reads its element, a field path into a RECORD column reads the
// JSON, and anything else stays a plain column reference.
func bqRewritePath(path []string, out []bqSQLToken, scope *bqScope) ([]bqSQLToken, error) {
	plain := func() []bqSQLToken {
		var tokens []bqSQLToken
		for i, p := range path {
			if i > 0 {
				tokens = append(tokens, bqPunctToken("."))
			}
			tokens = append(tokens, bqQuotedToken(p))
		}
		return tokens
	}
	extract := func(base []bqSQLToken, rest []string) []bqSQLToken {
		tokens := []bqSQLToken{bqWordToken("json_extract"), bqPunctToken("(")}
		tokens = append(tokens, base...)
		tokens = append(tokens, bqPunctToken(","), bqSpaceToken(), bqStringToken("$."+strings.Join(rest, ".")), bqPunctToken(")"))
		return tokens
	}
	if it := scope.item(path[0]); it != nil {
		switch {
		case it.unnest && len(path) == 1:
			return []bqSQLToken{bqQuotedToken(it.sqlName), bqPunctToken("."), bqWordToken("value")}, nil
		case it.unnest:
			return extract([]bqSQLToken{bqQuotedToken(it.sqlName), bqPunctToken("."), bqWordToken("value")}, path[1:]), nil
		case it.table != nil && len(path) >= 3:
			if f, ok := bqFieldAtPath(it.table.schema, path[1:2]); ok && bqType(f) == "STRUCT" {
				return extract([]bqSQLToken{bqQuotedToken(it.sqlName), bqPunctToken("."), bqQuotedToken(path[1])}, path[2:]), nil
			}
		}
		return plain(), nil
	}
	for _, it := range scope.items {
		if it.unnest && it.offset != "" && strings.EqualFold(it.offset, path[0]) && len(path) == 1 {
			return []bqSQLToken{bqQuotedToken(it.sqlName), bqPunctToken("."), bqWordToken("key")}, nil
		}
	}
	if len(path) >= 2 {
		if f, ok := scope.column(path[0]); ok && bqType(f) == "STRUCT" {
			return extract([]bqSQLToken{bqQuotedToken(path[0])}, path[1:]), nil
		}
	}
	return plain(), nil
}

// bqCurrentTokens fixes CURRENT_TIMESTAMP, CURRENT_DATE, CURRENT_DATETIME and
// CURRENT_TIME at the statement's start time.
func bqCurrentTokens(name string, zoneArgs []bqSQLToken, scope *bqScope) []bqSQLToken {
	now := []bqSQLToken{bqWordToken("bq_timestamp_micros"), bqPunctToken("("), {text: strconv.FormatInt(scope.now.UnixMicro(), 10), kind: bqTokNumber, fixed: true}, bqPunctToken(")")}
	if name == "CURRENT_TIMESTAMP" {
		return now
	}
	out := []bqSQLToken{bqWordToken(bqSQLiteName(strings.TrimPrefix(name, "CURRENT_"))), bqPunctToken("(")}
	out = append(out, now...)
	if len(bqTrimSpace(zoneArgs)) > 0 {
		out = append(out, bqPunctToken(","), bqSpaceToken())
		out = append(out, zoneArgs...)
	}
	return append(out, bqPunctToken(")"))
}

// bqReadInterval reads INTERVAL expr UNIT from i and answers the expression
// tokens, the unit and the index of the unit token.
func bqReadInterval(tokens []bqSQLToken, i int) ([]bqSQLToken, string, int, error) {
	depth := 0
	start := i + 1
	for j := start; j < len(tokens); j++ {
		t := tokens[j]
		switch {
		case t.isPunct("("):
			depth++
		case t.isPunct(")"):
			depth--
		case depth == 0 && t.kind == bqTokIdent && bqDateParts[t.upper()] && j > start:
			if bqTrimSpace(tokens[start:j]) == nil {
				break
			}
			return bqTrimSpace(tokens[start:j]), t.upper(), j, nil
		}
	}
	return nil, "", 0, bqQueryErr("Syntax error: Expected a date part after INTERVAL")
}

// bqRewriteExtract turns `part FROM expr [AT TIME ZONE tz]` into the
// registered EXTRACT's arguments.
func bqRewriteExtract(inner []bqSQLToken, scope *bqScope) ([]bqSQLToken, error) {
	from := -1
	depth := 0
	for i, t := range inner {
		switch {
		case t.isPunct("("):
			depth++
		case t.isPunct(")"):
			depth--
		case depth == 0 && t.isWord("FROM"):
			from = i
		}
		if from >= 0 {
			break
		}
	}
	if from < 0 {
		return nil, bqQueryErr("Syntax error: Expected FROM in EXTRACT")
	}
	part := strings.ReplaceAll(bqSQLText(bqTrimSpace(inner[:from])), " ", "")
	exprTokens := inner[from+1:]
	var zone []bqSQLToken
	depth = 0
	for i, t := range exprTokens {
		switch {
		case t.isPunct("("):
			depth++
		case t.isPunct(")"):
			depth--
		case depth == 0 && t.isWord("AT"):
			j := bqSignificant(exprTokens, i+1)
			k := bqSignificant(exprTokens, j+1)
			if j < len(exprTokens) && exprTokens[j].isWord("TIME") && k < len(exprTokens) && exprTokens[k].isWord("ZONE") {
				zone = exprTokens[k+1:]
				exprTokens = exprTokens[:i]
			}
		}
		if zone != nil {
			break
		}
	}
	expr, err := bqRewriteExpressions(bqTrimSpace(exprTokens), scope)
	if err != nil {
		return nil, err
	}
	out := []bqSQLToken{bqWordToken("bq_extract"), bqPunctToken("("), bqStringToken(strings.ToUpper(part)), bqPunctToken(","), bqSpaceToken()}
	out = append(out, expr...)
	if zone != nil {
		z, err := bqRewriteExpressions(bqTrimSpace(zone), scope)
		if err != nil {
			return nil, err
		}
		out = append(out, bqPunctToken(","), bqSpaceToken())
		out = append(out, z...)
	}
	return append(out, bqPunctToken(")")), nil
}

// bqRewriteCast turns `expr AS type` into the registered CAST with the type
// and what is known of the expression's type.
func bqRewriteCast(name string, inner []bqSQLToken, scope *bqScope) ([]bqSQLToken, error) {
	as := -1
	depth := 0
	for i, t := range inner {
		switch {
		case t.isPunct("("):
			depth++
		case t.isPunct(")"):
			depth--
		case depth == 0 && t.isWord("AS"):
			as = i
		}
	}
	if as < 0 {
		return nil, bqQueryErr("Syntax error: Expected AS in %s", name)
	}
	typeTokens := bqTrimSpace(inner[as+1:])
	for i, t := range typeTokens {
		if t.isWord("FORMAT") {
			return nil, bqQueryErr("The simulator does not support a format clause in %s", name)
		}
		if t.isWord("AT") && i > 0 {
			return nil, bqQueryErr("The simulator does not support AT TIME ZONE in %s", name)
		}
	}
	typeText := strings.ReplaceAll(bqSQLText(typeTokens), " ", "")
	if _, err := bqParseSQLType(typeText); err != nil {
		return nil, err
	}
	expr, err := bqRewriteExpressions(bqTrimSpace(inner[:as]), scope)
	if err != nil {
		return nil, err
	}
	hint := bqTypeHint(bqExprType(expr, scope))
	out := []bqSQLToken{bqWordToken(bqSQLiteName(name)), bqPunctToken("(")}
	out = append(out, expr...)
	out = append(out, bqPunctToken(","), bqSpaceToken(), bqStringToken(typeText), bqPunctToken(","), bqSpaceToken(), bqStringToken(hint), bqPunctToken(")"))
	return out, nil
}

// bqTypeHint encodes a field's type for a registered function.
func bqTypeHint(f BQFieldSchema) string {
	if f.Type == "" {
		return ""
	}
	f.Name = ""
	raw, err := json.Marshal(f)
	if err != nil {
		return ""
	}
	return string(raw)
}

// bqRewriteStruct turns STRUCT(expr AS name, ...) into json_object.
func bqRewriteStruct(inner []bqSQLToken, scope *bqScope) ([]bqSQLToken, error) {
	out := []bqSQLToken{bqWordToken("json_object"), bqPunctToken("(")}
	for i, arg := range bqSplitArgs(inner) {
		name, expr := bqSplitAlias(arg)
		if name == "" {
			if path, ok := bqAsPath(expr); ok {
				name = path[len(path)-1]
			}
		}
		if name == "" {
			return nil, bqQueryErr("The simulator requires every STRUCT field to be named: use expr AS name")
		}
		rewritten, err := bqRewriteExpressions(expr, scope)
		if err != nil {
			return nil, err
		}
		if i > 0 {
			out = append(out, bqPunctToken(","), bqSpaceToken())
		}
		out = append(out, bqStringToken(name), bqPunctToken(","), bqSpaceToken())
		out = append(out, bqWrapJSON(rewritten, scope)...)
	}
	return append(out, bqPunctToken(")")), nil
}

// bqWrapJSON makes an ARRAY or STRUCT value nest as JSON inside json_object
// and json_array rather than as a string.
func bqWrapJSON(expr []bqSQLToken, scope *bqScope) []bqSQLToken {
	if f := bqExprType(expr, scope); bqNested(f) || bqType(f) == "JSON" {
		out := []bqSQLToken{bqWordToken("json"), bqPunctToken("(")}
		out = append(out, expr...)
		return append(out, bqPunctToken(")"))
	}
	return expr
}

// bqSplitAlias separates a trailing `AS alias` or implicit alias from an
// expression.
func bqSplitAlias(item []bqSQLToken) (string, []bqSQLToken) {
	item = bqTrimSpace(item)
	n := len(item)
	if n >= 3 && item[n-1].isIdent() && item[n-2].isSpace() {
		prev := bqPrevSignificant(item, n-1)
		if prev >= 0 && item[prev].isWord("AS") {
			return bqIdentName(item[n-1]), bqTrimSpace(item[:prev])
		}
		if prev >= 0 && !bqIsOperatorWord(item[prev]) && !item[prev].isPunct(",") && !item[prev].isPunct("(") && !bqReservedWords[item[n-1].upper()] &&
			(item[prev].isIdent() || item[prev].isPunct(")") || item[prev].isPunct("]") || item[prev].kind == bqTokString || item[prev].kind == bqTokNumber) &&
			(!item[prev].isIdent() || prev+1 >= n || item[prev+1].isSpace()) {
			if _, isPath := bqAsPath(item); !isPath {
				return bqIdentName(item[n-1]), bqTrimSpace(item[:prev+1])
			}
		}
	}
	return "", item
}

// bqAsPath reports whether tokens are one identifier path.
func bqAsPath(tokens []bqSQLToken) ([]string, bool) {
	tokens = bqTrimSpace(tokens)
	if len(tokens) == 0 || len(tokens)%2 == 0 {
		return nil, false
	}
	var path []string
	for i, t := range tokens {
		if i%2 == 0 {
			if !t.isIdent() || bqReservedWords[t.upper()] && !t.fixed {
				return nil, false
			}
			name := t.text
			if t.kind == bqTokQuoted && t.fixed {
				name = bqIdentName(t)
			}
			path = append(path, name)
		} else if !t.isPunct(".") {
			return nil, false
		}
	}
	return path, true
}

// bqRewriteSubscript rewrites the array subscript whose array expression ends
// the tokens of out: the primary expression before `[` becomes the first
// argument of the registered operator.
func bqRewriteSubscript(out, inner []bqSQLToken) ([]bqSQLToken, error) {
	start := bqPrimaryStart(out, len(out))
	if start < 0 {
		return nil, bqQueryErr("Syntax error: Unexpected [")
	}
	array := append([]bqSQLToken(nil), out[start:]...)
	out = out[:start]
	keyword := "OFFSET"
	index := bqTrimSpace(inner)
	if len(index) >= 2 && index[0].isIdent() && index[1].isPunct("(") && bqMatchParen(index, 1) == len(index)-1 {
		switch index[0].upper() {
		case "OFFSET", "ORDINAL", "SAFE_OFFSET", "SAFE_ORDINAL":
			keyword = index[0].upper()
			index = bqTrimSpace(index[2 : len(index)-1])
		}
	}
	out = append(out, bqWordToken("bq_array_at"), bqPunctToken("("))
	out = append(out, array...)
	out = append(out, bqPunctToken(","), bqSpaceToken())
	out = append(out, index...)
	out = append(out, bqPunctToken(","), bqSpaceToken(), bqStringToken(keyword), bqPunctToken(")"))
	return out, nil
}

// bqPrimaryStart is the index where the primary expression ending at end
// begins: a parenthesized group with its function name, a subscript, a path
// or a literal.
func bqPrimaryStart(tokens []bqSQLToken, end int) int {
	i := bqPrevSignificant(tokens, end)
	if i < 0 {
		return -1
	}
	switch {
	case tokens[i].isPunct(")"):
		depth := 0
		for j := i; j >= 0; j-- {
			if tokens[j].isPunct(")") {
				depth++
			} else if tokens[j].isPunct("(") {
				depth--
				if depth == 0 {
					if k := bqPrevSignificant(tokens, j); k >= 0 && tokens[k].isIdent() && !bqIsOperatorWord(tokens[k]) {
						return k
					}
					return j
				}
			}
		}
		return -1
	case tokens[i].isIdent():
		for i >= 2 && tokens[i-1].isPunct(".") && tokens[i-2].isIdent() {
			i -= 2
		}
		return i
	default:
		return i
	}
}

// bqRewriteCall rewrites a function call whose arguments are inner: the
// registered functions take their SQLite names, date parts become literals,
// IF becomes iif, ARRAY_AGG's NULL handling picks its variant, and
// TO_JSON_STRING learns its argument's type.
func bqRewriteCall(name string, inner []bqSQLToken, scope *bqScope) ([]bqSQLToken, error) {
	plain := strings.TrimPrefix(name, "SAFE_")
	isSafe := strings.HasPrefix(name, "SAFE_") && plain != "CAST" && plain != "DIVIDE" && plain != "ADD" && plain != "SUBTRACT" && plain != "MULTIPLY" && plain != "NEGATE" && plain != "OFFSET" && plain != "ORDINAL"
	if !isSafe {
		plain = name
	}
	f, registered := bqFunctions[plain]
	if isSafe && (!registered || !f.safe) {
		if registered {
			return nil, bqQueryErr("Function %s does not support the SAFE. prefix", plain)
		}
		name = plain
	}
	sqlName := name
	switch {
	case plain == "IF":
		sqlName = "iif"
	case plain == "COUNTIF" || plain == "LOGICAL_AND" || plain == "LOGICAL_OR" || plain == "ANY_VALUE" || plain == "STRING_AGG":
		sqlName = bqSQLiteName(plain)
	case registered:
		sqlName = bqSQLiteName(name)
	}
	if plain == "ARRAY_AGG" {
		return bqRewriteArrayAgg(inner, scope)
	}
	var args []bqSQLToken
	if bqDatePartFunctions[plain] {
		for i, arg := range bqSplitArgs(inner) {
			if i > 0 {
				args = append(args, bqPunctToken(","), bqSpaceToken())
			}
			if part, ok := bqDatePartLiteral(arg); ok {
				args = append(args, bqStringToken(part))
				continue
			}
			rewritten, err := bqRewriteExpressions(arg, scope)
			if err != nil {
				return nil, err
			}
			args = append(args, rewritten...)
		}
	} else {
		var err error
		if args, err = bqRewriteExpressions(inner, scope); err != nil {
			return nil, err
		}
	}
	if plain == "STRING_AGG" {
		parts := bqSplitArgs(args)
		if len(parts) > 1 && len(parts[0]) > 0 && parts[0][0].isWord("DISTINCT") {
			return nil, bqQueryErr("The simulator does not support STRING_AGG(DISTINCT ...) with a delimiter")
		}
	}
	out := []bqSQLToken{bqWordToken(sqlName), bqPunctToken("(")}
	out = append(out, args...)
	if plain == "TO_JSON_STRING" {
		parts := bqSplitArgs(args)
		if len(parts) == 0 || len(parts) > 2 {
			return nil, bqQueryErr("No matching signature for function TO_JSON_STRING for argument count %d", len(parts))
		}
		out = []bqSQLToken{bqWordToken(sqlName), bqPunctToken("(")}
		out = append(out, parts[0]...)
		out = append(out, bqPunctToken(","), bqSpaceToken(), bqStringToken(bqTypeHint(bqExprType(parts[0], scope))))
		if len(parts) == 2 {
			out = append(out, bqPunctToken(","), bqSpaceToken())
			out = append(out, parts[1]...)
		}
	}
	return append(out, bqPunctToken(")")), nil
}

// bqDatePartLiteral reads a bare date part argument such as DAY or
// WEEK(MONDAY).
func bqDatePartLiteral(arg []bqSQLToken) (string, bool) {
	arg = bqTrimSpace(arg)
	if len(arg) == 1 && arg[0].kind == bqTokIdent && bqDateParts[arg[0].upper()] {
		return arg[0].upper(), true
	}
	if len(arg) == 4 && arg[0].isWord("WEEK") && arg[1].isPunct("(") && arg[2].kind == bqTokIdent && arg[3].isPunct(")") {
		return "WEEK(" + arg[2].upper() + ")", true
	}
	return "", false
}

// bqRewriteArrayAgg reads ARRAY_AGG's clauses: [DISTINCT] expr [IGNORE |
// RESPECT NULLS] [ORDER BY ...]; a LIMIT or WHERE clause is not supported.
func bqRewriteArrayAgg(inner []bqSQLToken, scope *bqScope) ([]bqSQLToken, error) {
	name := "ARRAY_AGG"
	var kept []bqSQLToken
	for i := 0; i < len(inner); i++ {
		t := inner[i]
		switch {
		case t.isWord("IGNORE") || t.isWord("RESPECT"):
			j := bqSignificant(inner, i+1)
			if j < len(inner) && inner[j].isWord("NULLS") {
				if t.isWord("IGNORE") {
					name = "ARRAY_AGG_IGNORE_NULLS"
				}
				i = j
				continue
			}
		case t.isWord("LIMIT"):
			return nil, bqQueryErr("The simulator does not support LIMIT inside ARRAY_AGG")
		case t.isWord("WHERE"):
			return nil, bqQueryErr("The simulator does not support WHERE inside ARRAY_AGG")
		}
		kept = append(kept, t)
	}
	args, err := bqRewriteExpressions(kept, scope)
	if err != nil {
		return nil, err
	}
	out := []bqSQLToken{bqWordToken(bqSQLiteName(name)), bqPunctToken("(")}
	out = append(out, args...)
	return append(out, bqPunctToken(")")), nil
}

// bqExprType is what the statement fixes about an expression's result type,
// read from rewritten tokens: a column's declared type, a registered or
// built-in function's result, a literal's type, a predicate's BOOL.
func bqExprType(tokens []bqSQLToken, scope *bqScope) BQFieldSchema {
	tokens = bqTrimSpace(tokens)
	if len(tokens) == 0 {
		return BQFieldSchema{}
	}
	if path, ok := bqAsPath(tokens); ok {
		return bqPathType(path, scope)
	}
	if len(tokens) == 1 {
		switch t := tokens[0]; {
		case t.kind == bqTokString:
			return BQFieldSchema{Type: "STRING"}
		case t.kind == bqTokNumber:
			if strings.ContainsAny(t.text, ".eE") {
				return BQFieldSchema{Type: "FLOAT64"}
			}
			return BQFieldSchema{Type: "INT64"}
		case t.isWord("TRUE") || t.isWord("FALSE") || (t.fixed && (strings.EqualFold(t.text, "TRUE") || strings.EqualFold(t.text, "FALSE"))):
			return BQFieldSchema{Type: "BOOL"}
		}
		return BQFieldSchema{}
	}
	if tokens[0].isPunct("(") && bqMatchParen(tokens, 0) == len(tokens)-1 {
		inner := bqTrimSpace(tokens[1 : len(tokens)-1])
		if len(inner) > 0 && inner[0].isWord("SELECT") || len(inner) > 0 && inner[0].fixed && strings.EqualFold(inner[0].text, "SELECT") {
			return bqSubqueryType(inner, scope)
		}
		return bqExprType(inner, scope)
	}
	if tokens[0].isIdent() && tokens[1].isPunct("(") && bqMatchParen(tokens, 1) == len(tokens)-1 {
		return bqCallType(tokens[0].text, bqSplitArgs(tokens[2:len(tokens)-1]), scope)
	}
	if tokens[0].isIdent() && tokens[1].isPunct("(") && bqMatchParen(tokens, 1) >= 0 {
		// A window function call: name(args) OVER (...).
		end := bqMatchParen(tokens, 1)
		if next := bqSignificant(tokens, end+1); next < len(tokens) && (tokens[next].isWord("OVER") || tokens[next].fixed && strings.EqualFold(tokens[next].text, "OVER")) {
			return bqCallType(tokens[0].text, bqSplitArgs(tokens[2:end]), scope)
		}
	}
	if tokens[0].isWord("CASE") || tokens[0].fixed && strings.EqualFold(tokens[0].text, "CASE") {
		depth, cases := 0, 0
		for i, t := range tokens {
			switch {
			case t.isPunct("("):
				depth++
			case t.isPunct(")"):
				depth--
			case depth == 0 && strings.EqualFold(t.text, "CASE") && t.isIdent():
				cases++
			case depth == 0 && strings.EqualFold(t.text, "END") && t.isIdent():
				cases--
			case depth == 0 && cases == 1 && (strings.EqualFold(t.text, "THEN") || strings.EqualFold(t.text, "ELSE")) && t.isIdent():
				j := i + 1
				d := 0
				for k := j; k < len(tokens); k++ {
					switch {
					case tokens[k].isPunct("("):
						d++
					case tokens[k].isPunct(")"):
						d--
					case d == 0 && tokens[k].isIdent() && (strings.EqualFold(tokens[k].text, "WHEN") || strings.EqualFold(tokens[k].text, "ELSE") || strings.EqualFold(tokens[k].text, "END")):
						if f := bqExprType(tokens[j:k], scope); f.Type != "" {
							return f
						}
						k = len(tokens)
					}
				}
			}
		}
		return BQFieldSchema{}
	}
	depth := 0
	arithmetic := false
	float := false
	for i, t := range tokens {
		switch {
		case t.isPunct("("), t.isPunct("["):
			depth++
		case t.isPunct(")"), t.isPunct("]"):
			depth--
		case depth > 0:
		case t.kind == bqTokPunct && strings.ContainsAny(t.text, "=<>!"), t.isIdent() && bqPredicateWords[strings.ToUpper(t.text)]:
			if !t.isPunct("<") && !t.isPunct(">") || i == 0 || !tokens[i-1].isWord("ARRAY") && !tokens[i-1].isWord("STRUCT") {
				return BQFieldSchema{Type: "BOOL"}
			}
		case t.isPunct("||"):
			return BQFieldSchema{Type: "STRING"}
		case t.isPunct("/"):
			return BQFieldSchema{Type: "FLOAT64"}
		case t.isPunct("+"), t.isPunct("-"), t.isPunct("*"):
			arithmetic = true
		}
	}
	if arithmetic {
		for _, operand := range bqSplitOperands(tokens) {
			switch f := bqExprType(operand, scope); bqType(f) {
			case "FLOAT64", "NUMERIC", "BIGNUMERIC":
				float = true
			case "INT64", "":
			default:
				return BQFieldSchema{}
			}
		}
		if float {
			return BQFieldSchema{Type: "FLOAT64"}
		}
		return BQFieldSchema{Type: "INT64"}
	}
	return BQFieldSchema{}
}

var bqPredicateWords = map[string]bool{
	"AND": true, "OR": true, "NOT": true, "IS": true, "IN": true, "LIKE": true, "BETWEEN": true, "EXISTS": true,
}

// bqSplitOperands splits an arithmetic expression on its depth-0 operators.
func bqSplitOperands(tokens []bqSQLToken) [][]bqSQLToken {
	var out [][]bqSQLToken
	depth, start := 0, 0
	for i, t := range tokens {
		switch {
		case t.isPunct("("), t.isPunct("["):
			depth++
		case t.isPunct(")"), t.isPunct("]"):
			depth--
		case depth == 0 && (t.isPunct("+") || t.isPunct("-") || t.isPunct("*")):
			if operand := bqTrimSpace(tokens[start:i]); len(operand) > 0 {
				out = append(out, operand)
			}
			start = i + 1
		}
	}
	if operand := bqTrimSpace(tokens[start:]); len(operand) > 0 {
		out = append(out, operand)
	}
	return out
}

// bqSubqueryType is the type of a scalar subquery: its single select item.
func bqSubqueryType(inner []bqSQLToken, scope *bqScope) BQFieldSchema {
	items, _, _, _ := bqSelectItems(inner)
	if len(items) == 1 {
		_, expr := bqSplitAlias(items[0])
		return bqExprType(expr, scope)
	}
	return BQFieldSchema{}
}

// bqPathType is the declared type of a column path.
func bqPathType(path []string, scope *bqScope) BQFieldSchema {
	if it := scope.item(path[0]); it != nil {
		switch {
		case it.unnest && len(path) == 1:
			return it.elem
		case it.unnest && strings.EqualFold(path[1], "value") && len(path) == 2:
			return it.elem
		case it.unnest && strings.EqualFold(path[1], "key") && len(path) == 2:
			return BQFieldSchema{Type: "INT64"}
		case it.unnest:
			if f, ok := bqFieldAtPath(it.elem.Fields, path[1:]); ok {
				return f
			}
			return BQFieldSchema{}
		case it.table != nil && len(path) >= 2:
			if f, ok := bqFieldAtPath(it.table.schema, path[1:]); ok {
				return f
			}
		}
		return BQFieldSchema{}
	}
	for _, it := range scope.items {
		if it.unnest && it.offset != "" && len(path) == 1 && strings.EqualFold(it.offset, path[0]) {
			return BQFieldSchema{Type: "INT64"}
		}
	}
	if f, ok := scope.column(path[0]); ok {
		if len(path) == 1 {
			return f
		}
		if sub, ok := bqFieldAtPath(f.Fields, path[1:]); ok && bqType(f) == "STRUCT" {
			return sub
		}
	}
	return BQFieldSchema{}
}

// bqCallType is the result type of a function call.
func bqCallType(name string, args [][]bqSQLToken, scope *bqScope) BQFieldSchema {
	lower := strings.ToLower(name)
	argType := func(i int) BQFieldSchema {
		if i < len(args) {
			arg := args[i]
			if len(arg) > 0 && (arg[0].isWord("DISTINCT") || arg[0].fixed && strings.EqualFold(arg[0].text, "DISTINCT")) {
				arg = bqTrimSpace(arg[1:])
			}
			depth := 0
			for j, t := range arg {
				switch {
				case t.isPunct("("):
					depth++
				case t.isPunct(")"):
					depth--
				case depth == 0 && t.isIdent() && strings.EqualFold(t.text, "ORDER"):
					arg = bqTrimSpace(arg[:j])
					depth = -100
				}
				if depth == -100 {
					break
				}
			}
			return bqExprType(arg, scope)
		}
		return BQFieldSchema{}
	}
	switch lower {
	case "json_extract":
		if len(args) == 2 && args[1][0].kind == bqTokString {
			base := bqExprType(args[0], scope)
			if path := strings.Split(strings.TrimPrefix(bqStringValue(args[1][0]), "$."), "."); bqType(base) == "STRUCT" {
				if f, ok := bqFieldAtPath(base.Fields, path); ok {
					return f
				}
			}
		}
		return BQFieldSchema{}
	case "json_array":
		f := BQFieldSchema{Mode: "REPEATED"}
		for _, a := range args {
			if elem := bqExprType(a, scope); elem.Type != "" {
				f.Type, f.Fields = elem.Type, elem.Fields
				break
			}
		}
		return f
	case "json_object":
		f := BQFieldSchema{Type: "RECORD", Mode: "NULLABLE"}
		for i := 0; i+1 < len(args); i += 2 {
			if len(args[i]) == 1 && args[i][0].kind == bqTokString {
				sub := bqExprType(bqUnwrapJSON(args[i+1]), scope)
				sub.Name = bqStringValue(args[i][0])
				if sub.Type == "" {
					sub.Type = "STRING"
				}
				if sub.Mode == "" {
					sub.Mode = "NULLABLE"
				}
				f.Fields = append(f.Fields, sub)
			}
		}
		return f
	case "json":
		if len(args) == 1 {
			return bqExprType(args[0], scope)
		}
		return BQFieldSchema{Type: "JSON"}
	case "json_group_array":
		f := argType(0)
		f.Mode = "REPEATED"
		return f
	case "iif", "coalesce", "ifnull", "nullif", "min", "max", "sum", "first_value", "last_value", "lag", "lead", "nth_value", "greatest", "least", "any_value":
		for i := range args {
			if lower == "iif" && i == 0 {
				continue
			}
			if f := argType(i); f.Type != "" {
				return f
			}
		}
		return BQFieldSchema{}
	case "count", "row_number", "rank", "dense_rank", "ntile", "length", "instr", "unicode", "octet_length":
		return BQFieldSchema{Type: "INT64"}
	case "avg", "total", "percent_rank", "cume_dist", "sqrt", "exp", "ln", "log", "log10", "log2", "pow", "power", "sin", "cos", "tan", "asin", "acos", "atan", "atan2", "pi", "radians", "degrees", "random":
		return BQFieldSchema{Type: "FLOAT64"}
	case "abs", "round", "ceil", "ceiling", "floor", "sign":
		return argType(0)
	case "lower", "upper", "trim", "ltrim", "rtrim", "substr", "substring", "replace", "printf", "format", "group_concat", "hex", "quote", "typeof", "char", "concat_ws", "left", "right", "translate", "soundex":
		return BQFieldSchema{Type: "STRING"}
	}
	if strings.HasPrefix(lower, "bq_") {
		fnName := strings.ToUpper(strings.TrimPrefix(lower, "bq_"))
		f, ok := bqFunctions[fnName]
		if !ok {
			if f, ok = bqFunctions[strings.TrimPrefix(fnName, "SAFE_")]; !ok {
				return BQFieldSchema{}
			}
		}
		switch fnName {
		case "CAST", "SAFE_CAST":
			if len(args) >= 2 && len(args[1]) == 1 && args[1][0].kind == bqTokString {
				if t, err := bqParseSQLType(bqStringValue(args[1][0])); err == nil {
					return t
				}
			}
			return BQFieldSchema{}
		case "EXTRACT":
			if len(args) >= 1 && len(args[0]) == 1 && args[0][0].kind == bqTokString {
				switch bqStringValue(args[0][0]) {
				case "DATE":
					return BQFieldSchema{Type: "DATE"}
				case "DATETIME":
					return BQFieldSchema{Type: "DATETIME"}
				case "TIME":
					return BQFieldSchema{Type: "TIME"}
				}
			}
			return BQFieldSchema{Type: "INT64"}
		}
		returns := f.returns
		switch {
		case strings.HasPrefix(returns, "ARRAY:ARG0"):
			elem := argType(0)
			elem.Mode = "REPEATED"
			return elem
		case strings.HasPrefix(returns, "ARRAY<"):
			t, err := bqParseSQLType(returns)
			if err == nil {
				return t
			}
			return BQFieldSchema{Mode: "REPEATED"}
		case strings.HasPrefix(returns, "ARG0"):
			if t := argType(0); t.Type != "" {
				t.Mode = "NULLABLE"
				if bqRepeated(argType(0)) {
					t.Mode = "REPEATED"
				}
				return t
			}
			if _, fallback, ok := strings.Cut(returns, ":"); ok {
				return BQFieldSchema{Type: fallback}
			}
			return BQFieldSchema{}
		case returns != "":
			return BQFieldSchema{Type: returns}
		}
	}
	return BQFieldSchema{}
}

// bqUnwrapJSON strips the json(...) wrapper bqWrapJSON added.
func bqUnwrapJSON(tokens []bqSQLToken) []bqSQLToken {
	tokens = bqTrimSpace(tokens)
	if len(tokens) >= 3 && tokens[0].fixed && strings.EqualFold(tokens[0].text, "json") && tokens[1].isPunct("(") && bqMatchParen(tokens, 1) == len(tokens)-1 {
		return bqTrimSpace(tokens[2 : len(tokens)-1])
	}
	return tokens
}

// bqSelectItems splits the outermost SELECT list of a query: it answers the
// items, the index of SELECT, the index where the items start and the index
// where the FROM clause (or what follows the list) starts. selectAt is -1
// when the query has no single outermost SELECT.
func bqSelectItems(tokens []bqSQLToken) (items [][]bqSQLToken, selectAt, itemsStart, itemsEnd int) {
	selectAt = -1
	depth := 0
	selects := 0
	for i, t := range tokens {
		switch {
		case t.isPunct("("):
			depth++
		case t.isPunct(")"):
			depth--
		case depth == 0 && t.isIdent() && strings.EqualFold(t.text, "SELECT"):
			selects++
			if selectAt < 0 {
				selectAt = i
			}
		}
	}
	if selectAt < 0 || selects != 1 {
		return nil, -1, 0, 0
	}
	itemsStart = selectAt + 1
	for {
		j := bqSignificant(tokens, itemsStart)
		if j < len(tokens) && (tokens[j].isWord("DISTINCT") || tokens[j].isWord("ALL")) {
			itemsStart = j + 1
			continue
		}
		break
	}
	itemsEnd = len(tokens)
	depth = 0
	for i := itemsStart; i < len(tokens); i++ {
		t := tokens[i]
		switch {
		case t.isPunct("("):
			depth++
		case t.isPunct(")"):
			depth--
		case depth == 0 && t.isIdent() && (strings.EqualFold(t.text, "FROM") || strings.EqualFold(t.text, "WHERE") || strings.EqualFold(t.text, "GROUP") ||
			strings.EqualFold(t.text, "ORDER") || strings.EqualFold(t.text, "LIMIT") || strings.EqualFold(t.text, "HAVING") || strings.EqualFold(t.text, "QUALIFY") || strings.EqualFold(t.text, "WINDOW")):
			itemsEnd = i
			i = len(tokens)
		}
	}
	items = bqSplitArgs(tokens[itemsStart:itemsEnd])
	if n := len(items); n > 0 && len(items[n-1]) == 0 {
		items = items[:n-1]
	}
	return items, selectAt, itemsStart, itemsEnd
}

// bqRewriteSelectStructure expands SELECT * (with EXCEPT and REPLACE, and
// over UNNEST range variables), names every result column, moves a QUALIFY
// clause into a filtering outer query, and records the result types.
func bqRewriteSelectStructure(tokens []bqSQLToken, scope *bqScope) ([]bqSQLToken, error) {
	items, selectAt, itemsStart, itemsEnd := bqSelectItems(tokens)
	if selectAt < 0 {
		scope.resultTypes = nil
		return tokens, nil
	}
	for _, it := range items {
		if len(it) >= 2 && it[0].isWord("AS") && (it[1].isWord("STRUCT") || it[1].isWord("VALUE")) {
			return nil, bqQueryErr("The simulator does not support SELECT AS %s", strings.ToUpper(it[1].text))
		}
	}
	var expanded []bqSelectColumn
	anonymous := 0
	for _, item := range items {
		cols, err := bqExpandSelectItem(item, scope, &anonymous)
		if err != nil {
			return nil, err
		}
		expanded = append(expanded, cols...)
	}
	rest := tokens[itemsEnd:]
	qualify, rest, tail, err := bqSplitQualify(rest)
	if err != nil {
		return nil, err
	}
	hidden := 0
	if qualify != nil {
		qualify, expanded, hidden = bqHoistWindows(qualify, expanded, scope)
	}
	var list []bqSQLToken
	types := make([]BQFieldSchema, 0, len(expanded))
	for i, col := range expanded {
		if i > 0 {
			list = append(list, bqPunctToken(","), bqSpaceToken())
		}
		list = append(list, col.expr...)
		if !col.star {
			list = append(list, bqSpaceToken(), bqWordToken("AS"), bqSpaceToken(), bqQuotedToken(col.name))
		}
		types = append(types, col.typ)
	}
	scope.resultTypes = types
	if hidden > 0 {
		scope.resultTypes = append(scope.resultTypes, make([]BQFieldSchema, hidden)...)
		copy(scope.resultTypes[len(types)-hidden:], types[len(types)-hidden:])
	}
	var out []bqSQLToken
	out = append(out, tokens[:itemsStart]...)
	out = append(out, bqSpaceToken())
	out = append(out, list...)
	out = append(out, bqSpaceToken())
	out = append(out, rest...)
	if qualify == nil {
		return append(out, tail...), nil
	}
	// A QUALIFY clause filters the window results: the query so far becomes
	// a subquery, the condition its WHERE, and ORDER BY and LIMIT follow.
	wrapped := []bqSQLToken{}
	wrapped = append(wrapped, tokens[:selectAt]...)
	wrapped = append(wrapped, bqWordToken("SELECT"), bqSpaceToken(), bqPunctToken("*"), bqSpaceToken(), bqWordToken("FROM"), bqSpaceToken(), bqPunctToken("("))
	wrapped = append(wrapped, out[selectAt:]...)
	wrapped = append(wrapped, bqPunctToken(")"), bqSpaceToken(), bqWordToken("WHERE"), bqSpaceToken())
	wrapped = append(wrapped, qualify...)
	wrapped = append(wrapped, bqSpaceToken())
	wrapped = append(wrapped, tail...)
	return wrapped, nil
}

// bqSelectColumn is one output column of the outermost SELECT.
type bqSelectColumn struct {
	expr []bqSQLToken
	name string
	typ  BQFieldSchema
	star bool
}

// bqExpandSelectItem names an item and expands a star.
func bqExpandSelectItem(item []bqSQLToken, scope *bqScope, anonymous *int) ([]bqSelectColumn, error) {
	item = bqTrimSpace(item)
	if len(item) == 0 {
		return nil, bqQueryErr("Syntax error: Unexpected empty select list item")
	}
	starAt := -1
	for i, t := range item {
		if t.isPunct("*") && (i == 0 || item[i-1].isPunct(".")) {
			starAt = i
			break
		}
	}
	if starAt < 0 {
		alias, expr := bqSplitAlias(item)
		name := alias
		if name == "" {
			unnestPart := len(expr) == 3 && expr[1].isPunct(".") && expr[2].fixed && expr[2].kind == bqTokIdent
			switch {
			case unnestPart && strings.EqualFold(expr[2].text, "value"):
				name = bqIdentName(expr[0])
			case unnestPart && strings.EqualFold(expr[2].text, "key"):
				for _, it := range scope.items {
					if it.unnest && strings.EqualFold(it.sqlName, bqIdentName(expr[0])) {
						name = it.offset
					}
				}
			case len(expr) >= 3 && expr[0].fixed && strings.EqualFold(expr[0].text, "json_extract"):
				if args := bqSplitArgs(expr[2 : len(expr)-1]); len(args) == 2 && args[1][0].kind == bqTokString {
					path := strings.Split(bqStringValue(args[1][0]), ".")
					name = path[len(path)-1]
				}
			default:
				if path, ok := bqAsPath(expr); ok {
					name = path[len(path)-1]
					if it := scope.item(path[0]); it != nil && it.unnest && len(path) == 1 {
						name = it.alias
					}
				}
			}
		}
		if name == "" {
			name = fmt.Sprintf("f%d_", *anonymous)
			*anonymous++
		}
		return []bqSelectColumn{{expr: expr, name: name, typ: bqExprType(expr, scope)}}, nil
	}
	qualifier := ""
	if starAt > 0 {
		path, ok := bqAsPath(item[:starAt-1])
		if !ok || len(path) != 1 {
			return nil, bqQueryErr("The simulator does not support %s", bqSQLText(item))
		}
		qualifier = path[0]
	}
	var except []string
	var replace [][]bqSQLToken
	rest := bqTrimSpace(item[starAt+1:])
	for len(rest) > 0 {
		open := bqSignificant(rest, 1)
		if open >= len(rest) || !rest[open].isPunct("(") {
			return nil, bqQueryErr("Syntax error: Expected ( after %s", rest[0].text)
		}
		end := bqMatchParen(rest, open)
		args := bqSplitArgs(rest[open+1 : end])
		switch {
		case rest[0].isWord("EXCEPT"):
			for _, a := range args {
				path, ok := bqAsPath(a)
				if !ok || len(path) != 1 {
					return nil, bqQueryErr("Syntax error: Expected a column name in EXCEPT")
				}
				except = append(except, path[0])
			}
		case rest[0].isWord("REPLACE"):
			replace = append(replace, args...)
		default:
			return nil, bqQueryErr("Syntax error: Unexpected %s after *", rest[0].text)
		}
		rest = bqTrimSpace(rest[end+1:])
	}
	var sources []bqFromItem
	for _, it := range scope.items {
		if it.depth != 0 {
			continue
		}
		if qualifier == "" || strings.EqualFold(it.alias, qualifier) {
			sources = append(sources, it)
		}
	}
	// A star over tables and UNNESTs expands against their schemas, so each
	// column keeps its declared type; a subquery or CTE source stays a bare
	// star.
	needExpansion := len(sources) > 0
	for _, it := range sources {
		if !it.unnest && it.table == nil {
			needExpansion = len(except) > 0 || len(replace) > 0 || qualifier != ""
		}
	}
	if !needExpansion {
		return []bqSelectColumn{{expr: item, star: true}}, nil
	}
	if len(sources) == 0 {
		return nil, bqQueryErr("Unrecognized name: %s", qualifier)
	}
	var cols []bqSelectColumn
	for _, it := range sources {
		switch {
		case it.unnest:
			if bqType(it.elem) == "STRUCT" && len(it.elem.Fields) > 0 {
				for _, f := range it.elem.Fields {
					cols = append(cols, bqSelectColumn{expr: []bqSQLToken{bqWordToken("json_extract"), bqPunctToken("("), bqQuotedToken(it.sqlName), bqPunctToken("."), bqWordToken("value"), bqPunctToken(","), bqSpaceToken(), bqStringToken("$." + f.Name), bqPunctToken(")")}, name: f.Name, typ: f})
				}
			} else {
				cols = append(cols, bqSelectColumn{expr: []bqSQLToken{bqQuotedToken(it.sqlName), bqPunctToken("."), bqWordToken("value")}, name: it.alias, typ: it.elem})
			}
			if it.offset != "" {
				cols = append(cols, bqSelectColumn{expr: []bqSQLToken{bqQuotedToken(it.sqlName), bqPunctToken("."), bqWordToken("key")}, name: it.offset, typ: BQFieldSchema{Type: "INT64"}})
			}
		case it.table != nil:
			for _, f := range it.table.schema {
				cols = append(cols, bqSelectColumn{expr: []bqSQLToken{bqQuotedToken(it.sqlName), bqPunctToken("."), bqQuotedToken(f.Name)}, name: f.Name, typ: f})
			}
		default:
			return nil, bqQueryErr("The simulator cannot expand * over %s, a subquery or CTE; name its columns", it.alias)
		}
	}
	var kept []bqSelectColumn
	for _, col := range cols {
		excluded := false
		for _, name := range except {
			excluded = excluded || strings.EqualFold(name, col.name)
		}
		if excluded {
			continue
		}
		for _, r := range replace {
			alias, expr := bqSplitAlias(r)
			if alias == "" {
				return nil, bqQueryErr("Syntax error: Expected expression AS identifier in REPLACE")
			}
			if strings.EqualFold(alias, col.name) {
				col.expr = expr
				col.typ = bqExprType(expr, scope)
			}
		}
		kept = append(kept, col)
	}
	return kept, nil
}

// bqSplitQualify separates a depth-0 QUALIFY clause from what precedes it
// and from the ORDER BY and LIMIT that follow it.
func bqSplitQualify(rest []bqSQLToken) (qualify, before, tail []bqSQLToken, err error) {
	depth := 0
	at := -1
	for i, t := range rest {
		switch {
		case t.isPunct("("):
			depth++
		case t.isPunct(")"):
			depth--
		case depth == 0 && t.isWord("QUALIFY"):
			at = i
		}
		if at >= 0 {
			break
		}
	}
	if at < 0 {
		return nil, rest, nil, nil
	}
	end := len(rest)
	depth = 0
	for i := at + 1; i < len(rest); i++ {
		t := rest[i]
		switch {
		case t.isPunct("("):
			depth++
		case t.isPunct(")"):
			depth--
		case depth == 0 && (t.isWord("ORDER") || t.isWord("LIMIT") || t.isWord("WINDOW")):
			end = i
			i = len(rest)
		}
	}
	return bqTrimSpace(rest[at+1 : end]), rest[:at], rest[end:], nil
}

// bqHoistWindows moves each window function call of a QUALIFY condition into
// a hidden select column and refers to it from the condition.
func bqHoistWindows(cond []bqSQLToken, cols []bqSelectColumn, scope *bqScope) ([]bqSQLToken, []bqSelectColumn, int) {
	var out []bqSQLToken
	hidden := 0
	for i := 0; i < len(cond); i++ {
		t := cond[i]
		if t.isIdent() && i+1 < len(cond) {
			open := bqSignificant(cond, i+1)
			if open < len(cond) && cond[open].isPunct("(") {
				end := bqMatchParen(cond, open)
				over := bqSignificant(cond, end+1)
				if end > 0 && over < len(cond) && cond[over].isIdent() && strings.EqualFold(cond[over].text, "OVER") {
					windowOpen := bqSignificant(cond, over+1)
					windowEnd := windowOpen
					if windowOpen < len(cond) && cond[windowOpen].isPunct("(") {
						windowEnd = bqMatchParen(cond, windowOpen)
					}
					name := fmt.Sprintf("%s%d", bqHiddenColumnPrefix, hidden)
					expr := append([]bqSQLToken(nil), cond[i:windowEnd+1]...)
					cols = append(cols, bqSelectColumn{expr: expr, name: name, typ: bqExprType(expr, scope)})
					out = append(out, bqQuotedToken(name))
					hidden++
					i = windowEnd
					continue
				}
			}
		}
		out = append(out, t)
	}
	return out, cols, hidden
}

// bqSelectItemTypes is what the structure rewrite recorded about the result
// columns.
func bqSelectItemTypes(_ []bqSQLToken, scope *bqScope) []BQFieldSchema {
	return scope.resultTypes
}
