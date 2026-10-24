package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/e6qu/sockerless-cloud/sim"
	_ "modernc.org/sqlite"
)

// A query runs in an in-memory SQLite database holding the tables it names:
// each BigQuery column becomes a column of the SQLite type that compares the
// same way (INT64 and BOOL as INTEGER, TIMESTAMP as epoch microseconds,
// FLOAT64 as REAL, a RECORD or REPEATED value as typed JSON), every table
// reference becomes the quoted name of its loaded copy, the GoogleSQL
// constructs SQLite lacks are rewritten onto registered functions and JSON
// operators, and SQLite evaluates the rest of the statement.

// bqQueryError carries a message BigQuery answers a query with, worded as
// BigQuery words it, and the ErrorProto reason a failed job reports.
type bqQueryError struct {
	message string
	reason  string
}

func (e *bqQueryError) Error() string { return e.message }

func bqQueryErr(format string, args ...any) error {
	return &bqQueryError{message: fmt.Sprintf(format, args...), reason: "invalidQuery"}
}

func bqQueryErrReason(reason, format string, args ...any) error {
	return &bqQueryError{message: fmt.Sprintf(format, args...), reason: reason}
}

// bqQueryTable is a table a statement reads or writes.
type bqQueryTable struct {
	ref    BQTableRef
	name   string
	schema []BQFieldSchema
}

// bqTokKind classifies a lexical token of a query.
type bqTokKind int

const (
	bqTokSpace bqTokKind = iota
	bqTokIdent
	bqTokQuoted
	bqTokString
	bqTokNumber
	bqTokPunct
)

// bqSQLToken is one lexical token of a query. A string token's text is its
// SQLite literal; fixed marks a token a rewrite produced, which later
// passes leave alone.
type bqSQLToken struct {
	text  string
	kind  bqTokKind
	fixed bool
}

func (t bqSQLToken) isIdent() bool { return t.kind == bqTokIdent || t.kind == bqTokQuoted }
func (t bqSQLToken) isSpace() bool { return t.kind == bqTokSpace }
func (t bqSQLToken) isPunct(p string) bool {
	return t.kind == bqTokPunct && t.text == p
}

// isWord reports whether t is the bare keyword word, case-insensitively.
func (t bqSQLToken) isWord(word string) bool {
	return t.kind == bqTokIdent && !t.fixed && strings.EqualFold(t.text, word)
}

func (t bqSQLToken) upper() string {
	if t.kind == bqTokIdent {
		return strings.ToUpper(t.text)
	}
	return ""
}

func bqWordToken(text string) bqSQLToken {
	return bqSQLToken{text: text, kind: bqTokIdent, fixed: true}
}
func bqPunctToken(text string) bqSQLToken {
	return bqSQLToken{text: text, kind: bqTokPunct, fixed: true}
}
func bqSpaceToken() bqSQLToken { return bqSQLToken{text: " ", kind: bqTokSpace} }

// bqStringToken is a string literal in SQLite's form.
func bqStringToken(s string) bqSQLToken {
	return bqSQLToken{text: "'" + strings.ReplaceAll(s, "'", "''") + "'", kind: bqTokString, fixed: true}
}

// bqQuotedToken is an identifier quoted for SQLite.
func bqQuotedToken(name string) bqSQLToken {
	return bqSQLToken{text: "`" + strings.ReplaceAll(name, "`", "``") + "`", kind: bqTokQuoted, fixed: true}
}

// bqIdentName is the name an identifier token spells, unquoted.
func bqIdentName(t bqSQLToken) string {
	if t.kind == bqTokQuoted && t.fixed {
		return strings.ReplaceAll(strings.TrimSuffix(strings.TrimPrefix(t.text, "`"), "`"), "``", "`")
	}
	return t.text
}

// bqStringValue is the value of a string token.
func bqStringValue(t bqSQLToken) string {
	s := strings.TrimSuffix(strings.TrimPrefix(t.text, "'"), "'")
	return strings.ReplaceAll(s, "''", "'")
}

// bqTokenizeSQL splits a query into tokens: string literals become SQLite
// literals (GoogleSQL escapes decoded, a bytes literal its stored base64),
// comments disappear, and nothing inside a literal reads as a reference.
func bqTokenizeSQL(query string) ([]bqSQLToken, error) {
	var out []bqSQLToken
	rs := []rune(query)
	for i := 0; i < len(rs); {
		c := rs[i]
		switch {
		case unicode.IsSpace(c):
			j := i
			for j < len(rs) && unicode.IsSpace(rs[j]) {
				j++
			}
			out = append(out, bqSQLToken{text: string(rs[i:j]), kind: bqTokSpace})
			i = j
		case c == '-' && i+1 < len(rs) && rs[i+1] == '-', c == '#':
			j := i
			for j < len(rs) && rs[j] != '\n' {
				j++
			}
			// A comment becomes blanks of its own length, so a token's
			// offset in the statement is the length of what precedes it.
			out = append(out, bqSQLToken{text: strings.Repeat(" ", j-i), kind: bqTokSpace})
			i = j
		case c == '/' && i+1 < len(rs) && rs[i+1] == '*':
			end := strings.Index(string(rs[i+2:]), "*/")
			if end < 0 {
				return nil, bqQueryErr("Syntax error: Unclosed comment")
			}
			skip := len([]rune(string(rs[i+2:])[:end])) + 4
			out = append(out, bqSQLToken{text: strings.Repeat(" ", skip), kind: bqTokSpace})
			i += skip
		case c == '\'' || c == '"':
			value, next, err := bqReadStringLiteral(rs, i, false, false)
			if err != nil {
				return nil, err
			}
			out = append(out, bqSQLToken{text: "'" + strings.ReplaceAll(value, "'", "''") + "'", kind: bqTokString})
			i = next
		case (c == 'r' || c == 'R' || c == 'b' || c == 'B') && i+1 < len(rs) && (rs[i+1] == '\'' || rs[i+1] == '"'),
			(c == 'r' || c == 'R' || c == 'b' || c == 'B') && i+2 < len(rs) && (rs[i+2] == '\'' || rs[i+2] == '"') &&
				(rs[i+1] == 'r' || rs[i+1] == 'R' || rs[i+1] == 'b' || rs[i+1] == 'B'):
			prefix := strings.ToLower(string(rs[i : i+1]))
			if rs[i+1] != '\'' && rs[i+1] != '"' {
				prefix = strings.ToLower(string(rs[i : i+2]))
			}
			raw, bytes := strings.Contains(prefix, "r"), strings.Contains(prefix, "b")
			value, next, err := bqReadStringLiteral(rs, i+len(prefix), raw, bytes)
			if err != nil {
				return nil, err
			}
			if bytes {
				value = base64.StdEncoding.EncodeToString([]byte(value))
			}
			out = append(out, bqSQLToken{text: "'" + strings.ReplaceAll(value, "'", "''") + "'", kind: bqTokString})
			i = next
		case c == '`':
			j := i + 1
			for j < len(rs) && rs[j] != '`' {
				j++
			}
			if j >= len(rs) {
				return nil, bqQueryErr("Syntax error: Unclosed identifier literal")
			}
			out = append(out, bqSQLToken{text: string(rs[i+1 : j]), kind: bqTokQuoted})
			i = j + 1
		case c == '_' || unicode.IsLetter(c):
			j := i
			for j < len(rs) && (rs[j] == '_' || unicode.IsLetter(rs[j]) || unicode.IsDigit(rs[j])) {
				j++
			}
			out = append(out, bqSQLToken{text: string(rs[i:j]), kind: bqTokIdent})
			i = j
		case unicode.IsDigit(c), c == '.' && i+1 < len(rs) && unicode.IsDigit(rs[i+1]) && (len(out) == 0 || !out[len(out)-1].isIdent()):
			j := i
			for j < len(rs) && (unicode.IsDigit(rs[j]) || rs[j] == '.') {
				j++
			}
			if j < len(rs) && (rs[j] == 'e' || rs[j] == 'E') {
				k := j + 1
				if k < len(rs) && (rs[k] == '+' || rs[k] == '-') {
					k++
				}
				if k < len(rs) && unicode.IsDigit(rs[k]) {
					for k < len(rs) && unicode.IsDigit(rs[k]) {
						k++
					}
					j = k
				}
			}
			out = append(out, bqSQLToken{text: string(rs[i:j]), kind: bqTokNumber})
			i = j
		default:
			if (c == '<' || c == '>' || c == '!') && i+1 < len(rs) && rs[i+1] == '=' || c == '<' && i+1 < len(rs) && rs[i+1] == '>' ||
				c == '|' && i+1 < len(rs) && rs[i+1] == '|' || (c == '<' || c == '>') && i+1 < len(rs) && rs[i+1] == c && !bqInsideTypeAngle(out) {
				out = append(out, bqSQLToken{text: string(rs[i : i+2]), kind: bqTokPunct})
				i += 2
				continue
			}
			out = append(out, bqSQLToken{text: string(c), kind: bqTokPunct})
			i++
		}
	}
	return out, nil
}

// bqInsideTypeAngle reports whether the tokens so far have an unclosed `<`
// of an ARRAY or STRUCT type, in which case `>>` closes two of them rather
// than shifting.
func bqInsideTypeAngle(tokens []bqSQLToken) bool {
	depth := 0
	for i, t := range tokens {
		if t.isPunct("<") && i > 0 && (tokens[i-1].isWord("ARRAY") || tokens[i-1].isWord("STRUCT")) {
			depth++
		} else if t.isPunct("<") && depth > 0 {
			depth++
		} else if t.isPunct(">") && depth > 0 {
			depth--
		}
	}
	return depth > 0
}

// bqReadStringLiteral reads a quoted literal starting at the quote at i,
// including the triple-quoted form, and decodes its escapes unless raw.
func bqReadStringLiteral(rs []rune, i int, raw, bytes bool) (string, int, error) {
	quote := rs[i]
	triple := i+2 < len(rs) && rs[i+1] == quote && rs[i+2] == quote
	j := i + 1
	if triple {
		j = i + 3
	}
	var b strings.Builder
	for j < len(rs) {
		c := rs[j]
		if c == quote {
			if !triple {
				return b.String(), j + 1, nil
			}
			if j+2 < len(rs) && rs[j+1] == quote && rs[j+2] == quote {
				return b.String(), j + 3, nil
			}
		}
		if c == '\\' && j+1 < len(rs) && !raw {
			j++
			switch rs[j] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case 'a':
				b.WriteByte('\a')
			case 'b':
				b.WriteByte('\b')
			case 'f':
				b.WriteByte('\f')
			case 'v':
				b.WriteByte('\v')
			case '\\', '\'', '"', '`', '?':
				b.WriteRune(rs[j])
			case 'x', 'X':
				if j+2 < len(rs) {
					if n, err := strconv.ParseUint(string(rs[j+1:j+3]), 16, 8); err == nil {
						if bytes {
							b.WriteByte(byte(n))
						} else {
							b.WriteRune(rune(n))
						}
						j += 2
					}
				}
			case 'u', 'U':
				width := 4
				if rs[j] == 'U' {
					width = 8
				}
				if j+width < len(rs) {
					if n, err := strconv.ParseUint(string(rs[j+1:j+1+width]), 16, 32); err == nil {
						b.WriteRune(rune(n))
						j += width
					}
				}
			default:
				if rs[j] >= '0' && rs[j] <= '7' && j+2 < len(rs) {
					if n, err := strconv.ParseUint(string(rs[j:j+3]), 8, 8); err == nil {
						b.WriteByte(byte(n))
						j += 2
					}
				} else {
					return "", 0, bqQueryErr("Syntax error: Illegal escape sequence: \\%c", rs[j])
				}
			}
			j++
			continue
		}
		if c == '\n' && !triple {
			return "", 0, bqQueryErr("Syntax error: Unclosed string literal")
		}
		b.WriteRune(c)
		j++
	}
	return "", 0, bqQueryErr("Syntax error: Unclosed string literal")
}

// bqSQLText joins tokens back into SQL.
func bqSQLText(tokens []bqSQLToken) string {
	var b strings.Builder
	for _, t := range tokens {
		if t.kind == bqTokQuoted && !t.fixed {
			b.WriteString("`" + strings.ReplaceAll(t.text, "`", "``") + "`")
			continue
		}
		b.WriteString(t.text)
	}
	return b.String()
}

// bqSignificant is the index of the next token from i that is not space, or
// len(tokens).
func bqSignificant(tokens []bqSQLToken, i int) int {
	for i < len(tokens) && tokens[i].isSpace() {
		i++
	}
	return i
}

// bqPrevSignificant is the index of the last token before i that is not
// space, or -1.
func bqPrevSignificant(tokens []bqSQLToken, i int) int {
	i--
	for i >= 0 && tokens[i].isSpace() {
		i--
	}
	return i
}

// bqMatchParen is the index of the `)` closing the `(` at open, or -1.
func bqMatchParen(tokens []bqSQLToken, open int) int {
	depth := 0
	for i := open; i < len(tokens); i++ {
		switch {
		case tokens[i].isPunct("("), tokens[i].isPunct("["):
			depth++
		case tokens[i].isPunct(")"), tokens[i].isPunct("]"):
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// bqSplitArgs splits tokens on the commas at depth 0, trimming space.
func bqSplitArgs(tokens []bqSQLToken) [][]bqSQLToken {
	var out [][]bqSQLToken
	depth := 0
	start := 0
	for i, t := range tokens {
		switch {
		case t.isPunct("("), t.isPunct("["):
			depth++
		case t.isPunct(")"), t.isPunct("]"):
			depth--
		case depth == 0 && t.isPunct(","):
			out = append(out, bqTrimSpace(tokens[start:i]))
			start = i + 1
		}
	}
	if rest := bqTrimSpace(tokens[start:]); len(rest) > 0 || len(out) > 0 {
		out = append(out, rest)
	}
	return out
}

func bqTrimSpace(tokens []bqSQLToken) []bqSQLToken {
	for len(tokens) > 0 && tokens[0].isSpace() {
		tokens = tokens[1:]
	}
	for len(tokens) > 0 && tokens[len(tokens)-1].isSpace() {
		tokens = tokens[:len(tokens)-1]
	}
	return tokens
}

// bqFromItem is one range variable of a statement's FROM clauses: a table or
// view, an UNNEST, or an opaque subquery or CTE.
type bqFromItem struct {
	alias   string
	table   *bqQueryTable
	unnest  bool
	elem    BQFieldSchema
	offset  string
	sqlName string
	// depth is the parenthesis depth the item appeared at: 0 for the
	// outermost statement's own FROM clause.
	depth int
}

// bqScope is what the statement's FROM clauses bring into view.
type bqScope struct {
	defaultProject string
	defaultDataset string
	tables         []bqQueryTable
	items          []bqFromItem
	ctes           map[string]bool
	seen           map[string]*bqQueryTable
	// now fixes CURRENT_TIMESTAMP and its siblings for the statement.
	now time.Time
	// parameters are the query's named and positional parameters as the
	// literal tokens that replace them.
	parameters map[string][]bqSQLToken
	positional [][]bqSQLToken
	nextParam  int
	depth      int
	// resultTypes is what the structure rewrite fixed about each result
	// column of the outermost SELECT, in column order.
	resultTypes []BQFieldSchema
	// fromDepth is the parenthesis depth bqRewriteFrom is working at, and
	// itemDepth the depth of the item it is reading.
	fromDepth int
	itemDepth int
}

func bqNewScope(defaultProject, defaultDataset string) *bqScope {
	return &bqScope{
		defaultProject: defaultProject,
		defaultDataset: defaultDataset,
		ctes:           map[string]bool{},
		seen:           map[string]*bqQueryTable{},
		now:            time.Now().UTC(),
	}
}

func (s *bqScope) item(alias string) *bqFromItem {
	for i := range s.items {
		if strings.EqualFold(s.items[i].alias, alias) {
			return &s.items[i]
		}
	}
	return nil
}

// column resolves an unqualified column name against every table in scope.
func (s *bqScope) column(name string) (BQFieldSchema, bool) {
	for _, it := range s.items {
		if it.table == nil {
			continue
		}
		for _, f := range it.table.schema {
			if strings.EqualFold(f.Name, name) {
				return f, true
			}
		}
	}
	return BQFieldSchema{}, false
}

// resolveTable loads the table a path names into the scope.
func (s *bqScope) resolveTable(path []string) (*bqQueryTable, error) {
	var parts []string
	for _, p := range path {
		parts = append(parts, strings.Split(p, ".")...)
	}
	path = parts
	ref := strings.Join(path, ".")
	if len(path) == 1 {
		if s.defaultDataset == "" {
			return nil, bqQueryErr("Table %q must be qualified with a dataset (e.g. dataset.table).", ref)
		}
		ref = s.defaultDataset + "." + ref
	}
	project, dataset, table, err := bqParseTableRef(s.defaultProject, strings.Replace(ref, ":", ".", 1))
	if err != nil {
		return nil, bqQueryErr("Invalid table name: %s", ref)
	}
	key := bqTableKey(project, dataset, table)
	if t, ok := s.seen[key]; ok {
		return t, nil
	}
	t, exists := bqLiveTable(key)
	if !exists {
		return nil, bqQueryErrReason("notFound", "Not found: Table %s:%s.%s was not found in location US", project, dataset, table)
	}
	qt := &bqQueryTable{ref: BQTableRef{ProjectID: project, DatasetID: dataset, TableID: table}, name: fmt.Sprintf("bq_table_%d", len(s.tables)), schema: bqSchemaFields(t.Schema)}
	s.seen[key] = qt
	s.tables = append(s.tables, *qt)
	return qt, nil
}

// bqLiveTable is the stored table at key unless it has expired, in which case
// it is deleted and reported absent, as BigQuery reclaims an expired table.
func bqLiveTable(key string) (BQTable, bool) {
	t, ok := bqTables.Get(key)
	if !ok {
		return t, false
	}
	if t.ExpirationTime != "" {
		if ms, err := strconv.ParseInt(t.ExpirationTime, 10, 64); err == nil && ms <= time.Now().UnixMilli() {
			bqTables.Delete(key)
			bqRows.Delete(key)
			return BQTable{}, false
		}
	}
	return t, true
}

// bqClauseWords end a FROM item's alias position.
var bqClauseWords = map[string]bool{
	"WHERE": true, "GROUP": true, "ORDER": true, "LIMIT": true, "HAVING": true, "ON": true, "USING": true, "UNION": true,
	"WINDOW": true, "QUALIFY": true, "JOIN": true, "INNER": true, "LEFT": true, "RIGHT": true, "FULL": true, "CROSS": true,
	"OUTER": true, "WITH": true, "INTERSECT": true, "EXCEPT": true, "SET": true, "WHEN": true, "THEN": true, "FOR": true,
	"TABLESAMPLE": true, "PIVOT": true, "UNPIVOT": true, "NATURAL": true, "OFFSET": true, "RETURNING": true,
}

// bqRewriteFrom resolves every range variable of the statement: a table
// reference becomes the quoted name of its loaded copy (a view its expanded
// query), an UNNEST a json_each over the array, and each one's alias is
// recorded in the scope.
func bqRewriteFrom(tokens []bqSQLToken, scope *bqScope) ([]bqSQLToken, error) {
	for i := 0; i+2 < len(tokens); i++ {
		if (tokens[i].isWord("WITH") || tokens[i].isPunct(",")) && bqNextIsCTE(tokens, i) {
			scope.ctes[tokens[bqSignificant(tokens, i+1)].text] = true
		}
	}
	// The FROM of EXTRACT(part FROM expr) names no table.
	extractFrom := map[int]bool{}
	for i, t := range tokens {
		if !t.isWord("EXTRACT") {
			continue
		}
		open := bqSignificant(tokens, i+1)
		if open >= len(tokens) || !tokens[open].isPunct("(") {
			continue
		}
		end := bqMatchParen(tokens, open)
		depth := 0
		for j := open + 1; j < end; j++ {
			switch {
			case tokens[j].isPunct("("):
				depth++
			case tokens[j].isPunct(")"):
				depth--
			case depth == 0 && tokens[j].isWord("FROM"):
				extractFrom[j] = true
			}
		}
	}
	var out []bqSQLToken
	expectItem := false
	inFromList := false
	depthAtFrom := 0
	depth := 0
	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		if extractFrom[i] {
			out = append(out, tok)
			continue
		}
		switch {
		case tok.isPunct("("):
			depth++
		case tok.isPunct(")"):
			depth--
			if depth < depthAtFrom {
				inFromList = false
			}
		}
		if tok.isSpace() {
			out = append(out, tok)
			continue
		}
		if tok.kind == bqTokPunct {
			if tok.text == "," && inFromList && depth == depthAtFrom {
				expectItem = true
			} else if tok.text == "(" && expectItem {
				// A subquery or a parenthesized join: opaque to the scope.
				end := bqMatchParen(tokens, i)
				if end < 0 {
					return nil, bqQueryErr("Syntax error: Unmatched (")
				}
				scope.fromDepth += depth
				inner, err := bqRewriteFrom(tokens[i+1:end], scope)
				scope.fromDepth -= depth
				if err != nil {
					return nil, err
				}
				out = append(out, tok)
				out = append(out, inner...)
				out = append(out, tokens[end])
				depth--
				alias, consumed := bqReadAlias(tokens, end+1)
				if alias != "" {
					scope.items = append(scope.items, bqFromItem{alias: alias, sqlName: alias, depth: scope.fromDepth + depth})
					out = append(out, bqSpaceToken(), bqWordToken("AS"), bqSpaceToken(), bqQuotedToken(alias))
				}
				i = end + consumed
				expectItem = false
				continue
			} else if tok.text != "," {
				expectItem = false
			}
			out = append(out, tok)
			continue
		}
		if tok.kind == bqTokIdent && !tok.fixed {
			switch tok.upper() {
			case "FROM", "JOIN":
				expectItem = true
				if tok.upper() == "FROM" {
					inFromList, depthAtFrom = true, depth
				}
				out = append(out, tok)
				continue
			case "INTO", "UPDATE", "USING", "TABLE", "MERGE", "INSERT":
				// INSERT INTO, UPDATE, MERGE [INTO] and USING name the DML
				// tables; TRUNCATE TABLE and the DDL statements name theirs
				// after TABLE.
				if prev := bqPrevSignificant(out, len(out)); prev < 0 || !out[prev].isWord("INTO") {
					expectItem = true
				}
				out = append(out, tok)
				continue
			}
			if bqClauseWords[tok.upper()] {
				if tok.upper() != "JOIN" {
					expectItem = false
				}
				if tok.upper() == "WHERE" || tok.upper() == "GROUP" || tok.upper() == "ORDER" || tok.upper() == "LIMIT" || tok.upper() == "QUALIFY" || tok.upper() == "WINDOW" || tok.upper() == "HAVING" {
					inFromList = false
				}
				out = append(out, tok)
				continue
			}
			if tok.upper() == "SELECT" {
				inFromList, expectItem = false, false
				out = append(out, tok)
				continue
			}
		}
		if !expectItem {
			out = append(out, tok)
			continue
		}
		expectItem = false
		scope.itemDepth = scope.fromDepth + depth
		if tok.isWord("UNNEST") {
			next := bqSignificant(tokens, i+1)
			if next >= len(tokens) || !tokens[next].isPunct("(") {
				return nil, bqQueryErr("Syntax error: Expected \"(\" after UNNEST")
			}
			end := bqMatchParen(tokens, next)
			if end < 0 {
				return nil, bqQueryErr("Syntax error: Unmatched (")
			}
			arrayTokens := tokens[next+1 : end]
			replacement, consumed, err := bqRewriteUnnestItem(tokens, end+1, arrayTokens, scope)
			if err != nil {
				return nil, err
			}
			out = append(out, replacement...)
			i = end + consumed
			continue
		}
		if tok.isWord("TABLE") || tok.isWord("IF") || tok.isWord("NOT") || tok.isWord("EXISTS") || tok.isWord("ONLY") {
			// DDL keywords between TABLE and the name.
			expectItem = true
			out = append(out, tok)
			continue
		}
		// An identifier path: a table, a CTE, or an implicit UNNEST of an
		// array column.
		path := []string{tok.text}
		j := i
		for j+2 < len(tokens) && tokens[j+1].isPunct(".") && tokens[j+2].isIdent() {
			path = append(path, tokens[j+2].text)
			j += 2
		}
		if len(path) == 1 && scope.ctes[path[0]] {
			alias, consumed := bqReadAlias(tokens, j+1)
			if alias == "" {
				alias = path[0]
			}
			scope.items = append(scope.items, bqFromItem{alias: alias, sqlName: alias, depth: scope.fromDepth + depth})
			out = append(out, tok)
			i = j + consumed
			continue
		}
		if it := scope.item(path[0]); it != nil && len(path) >= 2 && it.table != nil {
			if f, ok := bqFieldAtPath(it.table.schema, path[1:]); ok && bqRepeated(f) {
				arrayTokens := []bqSQLToken{bqQuotedToken(it.sqlName), bqPunctToken(".")}
				arrayTokens = append(arrayTokens, bqStructPathTokens(it.sqlName, path[1:], it.table.schema)...)
				replacement, consumed, err := bqRewriteUnnestItem(tokens, j+1, arrayTokens[2:], scope)
				if err != nil {
					return nil, err
				}
				out = append(out, replacement...)
				i = j + consumed
				continue
			}
		}
		table, err := scope.resolveTable(path)
		if err != nil {
			return nil, err
		}
		alias, consumed := bqReadAlias(tokens, j+1)
		if alias == "" {
			alias = table.ref.TableID
		}
		stored, _ := bqTables.Get(bqTableKey(table.ref.ProjectID, table.ref.DatasetID, table.ref.TableID))
		if stored.Type == "VIEW" {
			viewTokens, err := bqExpandView(stored, scope)
			if err != nil {
				return nil, err
			}
			out = append(out, bqPunctToken("("))
			out = append(out, viewTokens...)
			out = append(out, bqPunctToken(")"))
		} else {
			out = append(out, bqQuotedToken(table.name))
		}
		out = append(out, bqSpaceToken(), bqWordToken("AS"), bqSpaceToken(), bqQuotedToken(alias))
		scope.items = append(scope.items, bqFromItem{alias: alias, table: table, sqlName: alias, depth: scope.fromDepth + depth})
		i = j + consumed
	}
	return out, nil
}

func bqNextIsCTE(tokens []bqSQLToken, i int) bool {
	name := bqSignificant(tokens, i+1)
	as := bqSignificant(tokens, name+1)
	open := bqSignificant(tokens, as+1)
	return name < len(tokens) && tokens[name].isIdent() && as < len(tokens) && tokens[as].isWord("AS") && open < len(tokens) && tokens[open].isPunct("(")
}

// bqReadAlias reads an optional [AS] alias after a FROM item at i and
// reports how many tokens it spanned.
func bqReadAlias(tokens []bqSQLToken, i int) (string, int) {
	j := bqSignificant(tokens, i)
	if j >= len(tokens) {
		return "", 0
	}
	if tokens[j].isWord("AS") {
		k := bqSignificant(tokens, j+1)
		if k < len(tokens) && tokens[k].isIdent() {
			return tokens[k].text, k - i + 1
		}
		return "", 0
	}
	if tokens[j].isIdent() && !tokens[j].fixed && !bqClauseWords[tokens[j].upper()] && !strings.EqualFold(tokens[j].text, "WITH") {
		return tokens[j].text, j - i + 1
	}
	return "", 0
}

// bqRewriteUnnestItem turns UNNEST(array) [AS] alias [WITH OFFSET [AS] off]
// into json_each over the array and records the range variable.
func bqRewriteUnnestItem(tokens []bqSQLToken, after int, arrayTokens []bqSQLToken, scope *bqScope) ([]bqSQLToken, int, error) {
	alias, consumed := bqReadAlias(tokens, after)
	if alias == "" {
		return nil, 0, bqQueryErr("The simulator requires an alias on UNNEST; UNNEST without an alias is not supported")
	}
	elem := bqExprType(arrayTokens, scope)
	if elem.Type == "" {
		elem = BQFieldSchema{Mode: "REPEATED"}
	}
	elem.Mode = "NULLABLE"
	item := bqFromItem{alias: alias, unnest: true, elem: elem, sqlName: alias, depth: scope.itemDepth}
	j := bqSignificant(tokens, after+consumed)
	if j < len(tokens) && tokens[j].isWord("WITH") {
		k := bqSignificant(tokens, j+1)
		if k >= len(tokens) || !tokens[k].isWord("OFFSET") {
			return nil, 0, bqQueryErr("Syntax error: Expected OFFSET after WITH")
		}
		item.offset = "offset"
		offAlias, offConsumed := bqReadAlias(tokens, k+1)
		if offAlias != "" {
			item.offset = offAlias
		}
		consumed = k + 1 + offConsumed - after
	}
	scope.items = append(scope.items, item)
	inner, err := bqRewriteExpressions(arrayTokens, scope)
	if err != nil {
		return nil, 0, err
	}
	out := []bqSQLToken{bqWordToken("json_each"), bqPunctToken("(")}
	out = append(out, inner...)
	out = append(out, bqPunctToken(")"), bqSpaceToken(), bqWordToken("AS"), bqSpaceToken(), bqQuotedToken(alias))
	return out, consumed, nil
}

// bqExpandView rewrites a view's query for use as a subquery.
func bqExpandView(view BQTable, scope *bqScope) ([]bqSQLToken, error) {
	if scope.depth > 10 {
		return nil, bqQueryErr("Views are nested too deeply: %s", view.ID)
	}
	var def struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(view.View, &def); err != nil || def.Query == "" {
		return nil, bqQueryErr("View %s has no query", view.ID)
	}
	inner := bqNewScope(view.TableReference.ProjectID, "")
	inner.depth = scope.depth + 1
	inner.now = scope.now
	inner.seen = scope.seen
	inner.tables = scope.tables
	tokens, err := bqPrepareQuery(def.Query, inner)
	if err != nil {
		return nil, err
	}
	scope.tables = inner.tables
	return tokens, nil
}

// bqFieldAtPath follows a field path through RECORD columns.
func bqFieldAtPath(fields []BQFieldSchema, path []string) (BQFieldSchema, bool) {
	var found BQFieldSchema
	for i, name := range path {
		ok := false
		for _, f := range fields {
			if strings.EqualFold(f.Name, name) {
				found, ok = f, true
				break
			}
		}
		if !ok {
			return BQFieldSchema{}, false
		}
		if i < len(path)-1 {
			if bqType(found) != "STRUCT" {
				return BQFieldSchema{}, false
			}
			fields = found.Fields
		}
	}
	return found, true
}

// bqStructPathTokens is the SQLite expression for alias.col.sub...: the column
// itself, or json_extract into a RECORD column.
func bqStructPathTokens(alias string, path []string, schema []BQFieldSchema) []bqSQLToken {
	column := []bqSQLToken{bqQuotedToken(alias), bqPunctToken("."), bqQuotedToken(path[0])}
	if len(path) == 1 {
		return column
	}
	out := []bqSQLToken{bqWordToken("json_extract"), bqPunctToken("(")}
	out = append(out, column...)
	out = append(out, bqPunctToken(","), bqSpaceToken(), bqStringToken("$."+strings.Join(path[1:], ".")), bqPunctToken(")"))
	return out
}

// bqPrepareQuery rewrites a query statement into the SQL SQLite runs, with
// the scope recording what it reads.
func bqPrepareQuery(query string, scope *bqScope) ([]bqSQLToken, error) {
	tokens, err := bqTokenizeSQL(query)
	if err != nil {
		return nil, err
	}
	tokens, err = bqRewriteFrom(tokens, scope)
	if err != nil {
		return nil, err
	}
	tokens, err = bqRewriteExpressions(tokens, scope)
	if err != nil {
		return nil, err
	}
	return bqRewriteSelectStructure(tokens, scope)
}

// bqOpenEngine opens the in-memory database a statement runs in and loads
// the tables the scope resolved.
func bqOpenEngine(ctx context.Context, scope *bqScope) (*sql.DB, error) {
	bqEnsureFunctions()
	db, err := sql.Open("sqlite", "file:bq-"+sim.NewUUID()+"?mode=memory&cache=private")
	if err != nil {
		return nil, fmt.Errorf("open query engine: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := bqLoadQueryTables(ctx, db, scope.tables); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// bqEvaluateSQL runs a GoogleSQL query over the tables it names.
func bqEvaluateSQL(ctx context.Context, defaultProject, defaultDataset, query string) (*BQSchema, []map[string]any, error) {
	return bqEvaluateQueryWith(ctx, bqNewScope(defaultProject, defaultDataset), query)
}

func bqEvaluateQueryWith(ctx context.Context, scope *bqScope, query string) (*BQSchema, []map[string]any, error) {
	query = strings.TrimSuffix(strings.TrimSpace(query), ";")
	raw, err := bqTokenizeSQL(query)
	if err != nil {
		return nil, nil, err
	}
	if kind := bqStatementKind(raw); kind != "QUERY" {
		return nil, nil, bqQueryErr("Syntax error: Expected a query statement but got %s", kind)
	}
	for _, t := range raw {
		if t.isPunct(";") {
			return nil, nil, bqQueryErr("Syntax error: Expected end of input but got \";\"")
		}
	}
	tokens, err := bqPrepareQuery(query, scope)
	if err != nil {
		return nil, nil, err
	}
	db, err := bqOpenEngine(ctx, scope)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = db.Close() }()
	return bqRunSelect(ctx, db, tokens, scope)
}

// bqRunSelect runs a rewritten query and types its result.
func bqRunSelect(ctx context.Context, db *sql.DB, tokens []bqSQLToken, scope *bqScope) (*BQSchema, []map[string]any, error) {
	rows, err := db.QueryContext(ctx, bqSQLText(tokens))
	if err != nil {
		return nil, nil, bqEngineError(err)
	}
	defer func() { _ = rows.Close() }()
	columns, err := rows.Columns()
	if err != nil {
		return nil, nil, err
	}
	var raw [][]any
	for rows.Next() {
		cells := make([]any, len(columns))
		ptrs := make([]any, len(columns))
		for i := range cells {
			ptrs[i] = &cells[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, nil, err
		}
		raw = append(raw, cells)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, bqEngineError(err)
	}
	fields := bqResultFields(columns, bqSelectItemTypes(tokens, scope), raw)
	out := make([]map[string]any, 0, len(raw))
	keep := make([]int, 0, len(fields))
	for i, f := range fields {
		if !strings.HasPrefix(f.Name, bqHiddenColumnPrefix) {
			keep = append(keep, i)
		}
	}
	for _, cells := range raw {
		row := make(map[string]any, len(keep))
		for _, i := range keep {
			v, err := bqResultCell(fields[i], cells[i])
			if err != nil {
				return nil, nil, err
			}
			row[fields[i].Name] = v
		}
		out = append(out, row)
	}
	visible := make([]BQFieldSchema, 0, len(keep))
	for _, i := range keep {
		visible = append(visible, fields[i])
	}
	return &BQSchema{Fields: visible}, out, nil
}

// bqEngineError words a SQLite error as BigQuery words it: an error a
// registered function raised is reported as that error, anything else as a
// syntax error.
func bqEngineError(err error) error {
	msg := bqSQLiteMessage(err)
	if i := strings.Index(msg, bqRuntimeErrPrefix); i >= 0 {
		return bqQueryErr("%s", msg[i+len(bqRuntimeErrPrefix):])
	}
	return bqQueryErr("Syntax error: %s", msg)
}

func bqSQLiteMessage(err error) string {
	msg := err.Error()
	if i := strings.Index(msg, "SQL logic error: "); i >= 0 {
		msg = msg[i+len("SQL logic error: "):]
	}
	return strings.TrimSuffix(msg, " (1)")
}

// bqSQLiteType is the SQLite type that holds a column so it compares as
// BigQuery compares it.
func bqSQLiteType(f BQFieldSchema) string {
	if bqNested(f) {
		return "TEXT"
	}
	switch bqType(f) {
	case "INT64", "BOOL", "TIMESTAMP":
		return "INTEGER"
	case "FLOAT64":
		return "REAL"
	case "NUMERIC", "BIGNUMERIC":
		return "NUMERIC"
	}
	return "TEXT"
}

// bqSQLiteValue is a stored cell, in the form bqCoerce stores, as its SQLite
// column holds it.
func bqSQLiteValue(f BQFieldSchema, v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	if bqNested(f) {
		raw, err := json.Marshal(bqTypedJSON(f, v))
		return string(raw), err
	}
	text := bqScalarText(v)
	switch bqType(f) {
	case "INT64", "TIMESTAMP":
		return strconv.ParseInt(text, 10, 64)
	case "BOOL":
		if text == "true" {
			return int64(1), nil
		}
		return int64(0), nil
	case "FLOAT64":
		return strconv.ParseFloat(text, 64)
	}
	return text, nil
}

// bqTypedJSON is a stored cell as the JSON the engine holds nested values
// in: numbers as numbers, BOOL as true or false, a TIMESTAMP as its
// microseconds, so array elements and record fields compare by type.
func bqTypedJSON(f BQFieldSchema, v any) any {
	if v == nil {
		return nil
	}
	if bqRepeated(f) {
		items, _ := v.([]any)
		elem := f
		elem.Mode = "NULLABLE"
		out := make([]any, 0, len(items))
		for _, item := range items {
			out = append(out, bqTypedJSON(elem, item))
		}
		return out
	}
	switch bqType(f) {
	case "STRUCT":
		obj, _ := v.(map[string]any)
		out := bqOrderedObject{}
		for _, sub := range f.Fields {
			out = append(out, bqMember{sub.Name, bqTypedJSON(sub, obj[sub.Name])})
		}
		return out
	case "INT64", "TIMESTAMP":
		if n, err := strconv.ParseInt(bqScalarText(v), 10, 64); err == nil {
			return n
		}
	case "FLOAT64":
		if x, err := strconv.ParseFloat(bqScalarText(v), 64); err == nil && !math.IsInf(x, 0) && !math.IsNaN(x) {
			return x
		}
	case "NUMERIC", "BIGNUMERIC":
		if n, err := strconv.ParseInt(bqScalarText(v), 10, 64); err == nil && n > -(1<<53) && n < 1<<53 {
			return n
		}
		if x, err := strconv.ParseFloat(bqScalarText(v), 64); err == nil && bqFormatFloat(x) == bqTrimFraction(bqScalarText(v)) {
			return x
		}
	case "BOOL":
		return bqScalarText(v) == "true"
	}
	return bqScalarText(v)
}

// bqStoredFromJSON turns the typed JSON of a nested result back into the
// stored cell form under f; a field without a type takes the value's.
func bqStoredFromJSON(f BQFieldSchema, v any) any {
	if v == nil {
		return nil
	}
	if bqRepeated(f) {
		items, ok := v.([]any)
		if !ok {
			items = []any{v}
		}
		elem := f
		elem.Mode = "NULLABLE"
		out := make([]any, 0, len(items))
		for _, item := range items {
			out = append(out, bqStoredFromJSON(elem, item))
		}
		return out
	}
	if bqType(f) == "STRUCT" {
		obj, _ := v.(map[string]any)
		out := make(map[string]any, len(f.Fields))
		for _, sub := range f.Fields {
			out[sub.Name] = bqStoredFromJSON(sub, obj[sub.Name])
		}
		return out
	}
	switch x := v.(type) {
	case json.Number:
		if bqType(f) == "FLOAT64" {
			if fl, err := x.Float64(); err == nil {
				return bqFormatFloat(fl)
			}
		}
		if bqType(f) == "BOOL" {
			return strconv.FormatBool(string(x) != "0")
		}
		return string(x)
	case bool:
		return strconv.FormatBool(x)
	case string:
		return x
	case map[string]any, []any:
		return bqMustJSON(x)
	}
	return bqScalarText(v)
}

// bqInferJSONField types a nested result value the statement gave no type
// for, from the typed JSON the engine computed.
func bqInferJSONField(name string, v any) BQFieldSchema {
	f := BQFieldSchema{Name: name, Mode: "NULLABLE", Type: "STRING"}
	switch x := v.(type) {
	case json.Number:
		f.Type = "FLOAT"
		if _, err := x.Int64(); err == nil {
			f.Type = "INTEGER"
		}
	case bool:
		f.Type = "BOOLEAN"
	case []any:
		f.Mode = "REPEATED"
		f.Type = "STRING"
		for _, item := range x {
			if item != nil {
				elem := bqInferJSONField(name, item)
				f.Type, f.Fields = elem.Type, elem.Fields
				break
			}
		}
	case map[string]any:
		f.Type = "RECORD"
		names := make([]string, 0, len(x))
		for k := range x {
			names = append(names, k)
		}
		sortStrings(names)
		for _, k := range names {
			f.Fields = append(f.Fields, bqInferJSONField(k, x[k]))
		}
	}
	return f
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func bqLoadQueryTables(ctx context.Context, db *sql.DB, tables []bqQueryTable) error {
	for _, t := range tables {
		if len(t.schema) == 0 {
			return bqQueryErr("Table %s:%s.%s has no schema", t.ref.ProjectID, t.ref.DatasetID, t.ref.TableID)
		}
		cols := make([]string, len(t.schema))
		marks := make([]string, len(t.schema))
		for i, f := range t.schema {
			cols[i] = fmt.Sprintf(`"%s" %s`, f.Name, bqSQLiteType(f))
			marks[i] = "?"
		}
		if _, err := db.ExecContext(ctx, fmt.Sprintf(`CREATE TABLE "%s" (%s)`, t.name, strings.Join(cols, ", "))); err != nil {
			return fmt.Errorf("load %s: %w", t.ref.TableID, err)
		}
		stored, _ := bqRows.Get(bqTableKey(t.ref.ProjectID, t.ref.DatasetID, t.ref.TableID))
		insert, err := db.PrepareContext(ctx, fmt.Sprintf(`INSERT INTO "%s" VALUES (%s)`, t.name, strings.Join(marks, ", ")))
		if err != nil {
			return fmt.Errorf("load %s: %w", t.ref.TableID, err)
		}
		for _, row := range stored.Rows {
			args := make([]any, len(t.schema))
			for i, f := range t.schema {
				v, err := bqSQLiteValue(f, row[f.Name])
				if err != nil {
					_ = insert.Close()
					return fmt.Errorf("load %s: %w", t.ref.TableID, err)
				}
				args[i] = v
			}
			if _, err := insert.ExecContext(ctx, args...); err != nil {
				_ = insert.Close()
				return fmt.Errorf("load %s: %w", t.ref.TableID, err)
			}
		}
		if err := insert.Close(); err != nil {
			return err
		}
	}
	return nil
}

// bqReadTableRows reads a loaded table back from the engine in the stored
// cell form, in rowid order.
func bqReadTableRows(ctx context.Context, db *sql.DB, t bqQueryTable) ([]map[string]any, error) {
	cols := make([]string, len(t.schema))
	for i, f := range t.schema {
		cols[i] = `"` + f.Name + `"`
	}
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`SELECT %s FROM "%s" ORDER BY rowid`, strings.Join(cols, ", "), t.name))
	if err != nil {
		return nil, bqEngineError(err)
	}
	defer func() { _ = rows.Close() }()
	var out []map[string]any
	for rows.Next() {
		cells := make([]any, len(t.schema))
		ptrs := make([]any, len(cells))
		for i := range cells {
			ptrs[i] = &cells[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make(map[string]any, len(t.schema))
		for i, f := range t.schema {
			v, err := bqResultCell(f, cells[i])
			if err != nil {
				return nil, err
			}
			row[f.Name] = v
		}
		out = append(out, row)
	}
	if out == nil {
		out = []map[string]any{}
	}
	return out, rows.Err()
}

// bqResultFields types each result column: an item whose type the statement
// fixes keeps it, and any other takes the type of the values SQLite computed.
func bqResultFields(columns []string, types []BQFieldSchema, raw [][]any) []BQFieldSchema {
	fields := make([]BQFieldSchema, len(columns))
	if len(types) != len(columns) {
		types = nil
	}
	for i, name := range columns {
		if types != nil && types[i].Type != "" {
			f := bqAPIField(types[i])
			f.Name = name
			if f.Mode == "REQUIRED" {
				f.Mode = "NULLABLE"
			}
			fields[i] = f
			continue
		}
		fields[i] = BQFieldSchema{Name: name, Type: "STRING", Mode: "NULLABLE"}
		if types != nil && types[i].Mode == "REPEATED" {
			fields[i].Mode = "REPEATED"
		}
	cells:
		for _, cells := range raw {
			switch v := cells[i].(type) {
			case nil:
				continue
			case int64:
				fields[i].Type = "INTEGER"
			case float64:
				fields[i].Type = "FLOAT"
			case []byte:
				fields[i].Type = "BYTES"
			case string:
				if fields[i].Mode == "REPEATED" || (strings.HasPrefix(v, "[") || strings.HasPrefix(v, "{")) && json.Valid([]byte(v)) && (types != nil && types[i].Mode == "REPEATED") {
					var decoded any
					dec := json.NewDecoder(strings.NewReader(v))
					dec.UseNumber()
					if dec.Decode(&decoded) == nil {
						inferred := bqInferJSONField(name, decoded)
						fields[i].Type, fields[i].Fields, fields[i].Mode = inferred.Type, inferred.Fields, inferred.Mode
					}
				}
			}
			break cells
		}
	}
	return fields
}

// bqAPIField spells a field's types as a table schema does (INTEGER, FLOAT,
// BOOLEAN, RECORD) and gives every field a mode.
func bqAPIField(f BQFieldSchema) BQFieldSchema {
	if name, ok := bqSQLTypeNames[strings.ToUpper(f.Type)]; ok {
		f.Type = name
	}
	if f.Mode == "" {
		f.Mode = "NULLABLE"
	}
	if len(f.Fields) > 0 {
		sub := make([]BQFieldSchema, len(f.Fields))
		for i, s := range f.Fields {
			sub[i] = bqAPIField(s)
		}
		f.Fields = sub
	}
	return f
}

// bqResultCell turns a SQLite value back into the stored cell form of its
// result column.
func bqResultCell(f BQFieldSchema, v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	if bqNested(f) {
		var text string
		switch x := v.(type) {
		case string:
			text = x
		case []byte:
			text = string(x)
		default:
			return nil, bqQueryErr("column %s holds %s, not an ARRAY or STRUCT", f.Name, bqSQLiteTypeName(v))
		}
		dec := json.NewDecoder(strings.NewReader(text))
		dec.UseNumber()
		var decoded any
		if err := dec.Decode(&decoded); err != nil {
			return nil, bqQueryErr("column %s holds %q, not an ARRAY or STRUCT", f.Name, text)
		}
		return bqStoredFromJSON(f, decoded), nil
	}
	if b, ok := v.([]byte); ok {
		if bqType(f) == "BYTES" {
			return base64.StdEncoding.EncodeToString(b), nil
		}
		v = string(b)
	}
	switch bqType(f) {
	case "BOOL":
		if n, ok := v.(int64); ok {
			return strconv.FormatBool(n != 0), nil
		}
	case "TIMESTAMP":
		if n, ok := v.(int64); ok {
			return strconv.FormatInt(n, 10), nil
		}
		if s, ok := v.(string); ok {
			if t, err := bqParseTimestamp(s, s); err == nil {
				return strconv.FormatInt(t.UnixMicro(), 10), nil
			}
		}
	case "INT64":
		switch n := v.(type) {
		case int64:
			return strconv.FormatInt(n, 10), nil
		case float64:
			if n == math.Trunc(n) {
				return strconv.FormatInt(int64(n), 10), nil
			}
		}
	case "FLOAT64":
		switch n := v.(type) {
		case float64:
			return bqFormatFloat(n), nil
		case int64:
			return bqFormatFloat(float64(n)), nil
		}
	case "DATETIME":
		if s, ok := v.(string); ok {
			return strings.Replace(s, " ", "T", 1), nil
		}
	}
	switch n := v.(type) {
	case int64:
		return strconv.FormatInt(n, 10), nil
	case float64:
		return bqFormatFloat(n), nil
	case time.Time:
		return strconv.FormatInt(n.UnixMicro(), 10), nil
	}
	return fmt.Sprint(v), nil
}
