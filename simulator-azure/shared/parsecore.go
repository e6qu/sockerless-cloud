package simulator

import "strings"

// Shared bounds-safe scaffolding for the hand-rolled expression/DSL parsers
// (DynamoDB expressions, CloudWatch Logs Insights/filter-pattern, GCP AIP-160
// filter, Azure OData $filter, …). The grammars differ per cloud, but the
// UNSAFE parts are identical everywhere and are exactly what fuzzing keeps
// finding: raw s[i] / s[i:j] indexing that panics on a short/multibyte input,
// and unbounded recursion that blows the stack. Scanner removes the index
// panics by construction; ParseGuard bounds recursion + total work.

// Scanner is a bounds-safe cursor over a string. Every accessor is range-checked
// so a parser can never panic on an index or slice, regardless of input.
type Scanner struct {
	s   string
	pos int
}

// NewScanner returns a Scanner positioned at the start of s.
func NewScanner(s string) *Scanner { return &Scanner{s: s} }

// Pos / Len / SetPos manage the cursor; SetPos clamps into [0, len].
func (sc *Scanner) Pos() int { return sc.pos }
func (sc *Scanner) Len() int { return len(sc.s) }
func (sc *Scanner) SetPos(p int) {
	switch {
	case p < 0:
		sc.pos = 0
	case p > len(sc.s):
		sc.pos = len(sc.s)
	default:
		sc.pos = p
	}
}

// Eof reports whether the cursor is at or past the end.
func (sc *Scanner) Eof() bool { return sc.pos >= len(sc.s) }

// Rest returns the unconsumed suffix (never panics — pos is always clamped).
func (sc *Scanner) Rest() string { return sc.s[sc.pos:] }

// Peek returns the current byte without advancing, or 0 at EOF.
func (sc *Scanner) Peek() byte {
	if sc.pos < len(sc.s) {
		return sc.s[sc.pos]
	}
	return 0
}

// PeekAt returns the byte at offset n from the cursor, or 0 if out of range.
func (sc *Scanner) PeekAt(n int) byte {
	i := sc.pos + n
	if i >= 0 && i < len(sc.s) {
		return sc.s[i]
	}
	return 0
}

// Next returns the current byte and advances, or 0 at EOF (cursor unchanged).
func (sc *Scanner) Next() byte {
	if sc.pos < len(sc.s) {
		b := sc.s[sc.pos]
		sc.pos++
		return b
	}
	return 0
}

// Slice returns s[start:end] with both bounds clamped into range; an inverted
// range yields "".
func (sc *Scanner) Slice(start, end int) string {
	if start < 0 {
		start = 0
	}
	if end > len(sc.s) {
		end = len(sc.s)
	}
	if start >= end {
		return ""
	}
	return sc.s[start:end]
}

// SkipSpace advances over ASCII spaces and tabs.
func (sc *Scanner) SkipSpace() {
	for sc.pos < len(sc.s) {
		c := sc.s[sc.pos]
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			return
		}
		sc.pos++
	}
}

// HasPrefix reports whether the remaining input starts with p.
func (sc *Scanner) HasPrefix(p string) bool { return strings.HasPrefix(sc.s[sc.pos:], p) }

// HasPrefixFold is HasPrefix with ASCII-case-insensitive matching (byte-length
// preserving via ASCIIFold, so it stays slice-safe).
func (sc *Scanner) HasPrefixFold(p string) bool {
	if sc.pos+len(p) > len(sc.s) {
		return false
	}
	return ASCIIFold(sc.s[sc.pos:sc.pos+len(p)]) == ASCIIFold(p)
}

// ConsumePrefix advances past p when the remaining input starts with it.
func (sc *Scanner) ConsumePrefix(p string) bool {
	if sc.HasPrefix(p) {
		sc.pos += len(p)
		return true
	}
	return false
}

// DefaultMaxParseDepth / DefaultMaxParseNodes are sane caps for the sims'
// expression parsers — far above any real query, far below stack/OOM limits.
const (
	DefaultMaxParseDepth = 512
	DefaultMaxParseNodes = 100000
)

// ParseGuard bounds recursion depth and total node count for a recursive-descent
// parser so deeply-nested or pathological input can't blow the stack or hang.
// Enter() at the top of each recursive production, Leave() (usually deferred) on
// the way out; bail out of the parse when Enter() returns false.
type ParseGuard struct {
	depth, maxDepth int
	nodes, maxNodes int
}

// NewParseGuard returns a guard with the given caps (0 = the Default caps).
func NewParseGuard(maxDepth, maxNodes int) *ParseGuard {
	if maxDepth <= 0 {
		maxDepth = DefaultMaxParseDepth
	}
	if maxNodes <= 0 {
		maxNodes = DefaultMaxParseNodes
	}
	return &ParseGuard{maxDepth: maxDepth, maxNodes: maxNodes}
}

// Enter records one level of recursion / one node and reports whether the parse
// is still within budget. A false return means the input is too deep/large and
// the parser must stop (treat as a parse error).
func (g *ParseGuard) Enter() bool {
	g.depth++
	g.nodes++
	return g.depth <= g.maxDepth && g.nodes <= g.maxNodes
}

// Leave undoes one Enter's depth (not the node count — total work stays capped).
func (g *ParseGuard) Leave() {
	if g.depth > 0 {
		g.depth--
	}
}

// Exceeded reports whether any budget has been blown.
func (g *ParseGuard) Exceeded() bool { return g.depth > g.maxDepth || g.nodes > g.maxNodes }
