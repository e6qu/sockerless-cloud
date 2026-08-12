package simulator

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

// Pos / Len report the cursor position and the input length.
func (sc *Scanner) Pos() int { return sc.pos }
func (sc *Scanner) Len() int { return len(sc.s) }

// Eof reports whether the cursor is at or past the end.
func (sc *Scanner) Eof() bool { return sc.pos >= len(sc.s) }

// Peek returns the current byte without advancing, or 0 at EOF.
func (sc *Scanner) Peek() byte {
	if sc.pos < len(sc.s) {
		return sc.s[sc.pos]
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
