package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/dlclark/regexp2"
)

// substituteString takes java.util.regex.Pattern syntax; the guide links the
// Java SE 17 Pattern documentation and adds Python-style (?P<name>...) groups.
// cwjCompile parses that syntax and emits an equivalent pattern for regexp2's
// backtracking engine, with every character class resolved to explicit code
// point ranges, so Java's ASCII-by-default classes, its line terminators,
// possessive quantifiers, class unions and intersections, and its
// left-to-right group numbering hold.

type cwjRegex struct {
	re     *regexp2.Regexp
	groups int
	names  map[string]int
}

func (r *cwjRegex) group(name string) (int, bool) {
	n, ok := r.names[name]
	return n, ok
}

const (
	cwjFlagI  = 1 << iota // CASE_INSENSITIVE
	cwjFlagM              // MULTILINE
	cwjFlagS              // DOTALL
	cwjFlagX              // COMMENTS
	cwjFlagU              // UNICODE_CASE
	cwjFlagD              // UNIX_LINES
	cwjFlagUC             // UNICODE_CHARACTER_CLASS
)

type cwjRange struct{ lo, hi rune }

type cwjSet []cwjRange

func cwjNorm(s cwjSet) cwjSet {
	if len(s) == 0 {
		return nil
	}
	sort.Slice(s, func(i, j int) bool { return s[i].lo < s[j].lo })
	out := cwjSet{s[0]}
	for _, r := range s[1:] {
		last := &out[len(out)-1]
		if r.lo <= last.hi+1 {
			last.hi = max(last.hi, r.hi)
			continue
		}
		out = append(out, r)
	}
	return out
}

func cwjUnion(a, b cwjSet) cwjSet { return cwjNorm(append(append(cwjSet{}, a...), b...)) }

func cwjNegate(s cwjSet) cwjSet {
	s = cwjNorm(s)
	var out cwjSet
	next := rune(0)
	for _, r := range s {
		if r.lo > next {
			out = append(out, cwjRange{next, r.lo - 1})
		}
		next = r.hi + 1
	}
	if next <= unicode.MaxRune {
		out = append(out, cwjRange{next, unicode.MaxRune})
	}
	return out
}

func cwjIntersect(a, b cwjSet) cwjSet { return cwjNegate(cwjUnion(cwjNegate(a), cwjNegate(b))) }

func cwjRunes(rs ...rune) cwjSet {
	var s cwjSet
	for _, r := range rs {
		s = append(s, cwjRange{r, r})
	}
	return cwjNorm(s)
}

func cwjTable(tables ...*unicode.RangeTable) cwjSet {
	var s cwjSet
	for _, t := range tables {
		for _, r := range t.R16 {
			for c := rune(r.Lo); c <= rune(r.Hi); c += rune(r.Stride) {
				if r.Stride == 1 {
					s = append(s, cwjRange{rune(r.Lo), rune(r.Hi)})
					break
				}
				s = append(s, cwjRange{c, c})
			}
		}
		for _, r := range t.R32 {
			for c := rune(r.Lo); c <= rune(r.Hi); c += rune(r.Stride) {
				if r.Stride == 1 {
					s = append(s, cwjRange{rune(r.Lo), rune(r.Hi)})
					break
				}
				s = append(s, cwjRange{c, c})
			}
		}
	}
	return cwjNorm(s)
}

// cwjFold adds each character's case variants: ASCII only, unless UNICODE_CASE.
func cwjFold(s cwjSet, unicodeCase bool) cwjSet {
	out := append(cwjSet{}, s...)
	for _, r := range s {
		for c := r.lo; c <= r.hi; c++ {
			if !unicodeCase && c > 0x7f {
				break
			}
			for f := unicode.SimpleFold(c); f != c; f = unicode.SimpleFold(f) {
				if unicodeCase || f <= 0x7f {
					out = append(out, cwjRange{f, f})
				}
			}
		}
	}
	return cwjNorm(out)
}

func cwjEmitSet(s cwjSet) string {
	s = cwjNorm(s)
	if len(s) == 0 {
		return "(?!)"
	}
	var b strings.Builder
	b.WriteByte('[')
	for _, r := range s {
		fmt.Fprintf(&b, `\x{%X}`, r.lo)
		if r.hi > r.lo {
			fmt.Fprintf(&b, `-\x{%X}`, r.hi)
		}
	}
	b.WriteByte(']')
	return b.String()
}

var (
	cwjLineTerminators = cwjRunes('\n', '\r', 0x85, 0x2028, 0x2029)
	cwjASCIIDigit      = cwjSet{{'0', '9'}}
	cwjASCIIWord       = cwjSet{{'0', '9'}, {'A', 'Z'}, {'_', '_'}, {'a', 'z'}}
	cwjASCIISpace      = cwjRunes(' ', '\t', '\n', 0x0b, '\f', '\r')
	cwjHorizontal      = cwjNorm(cwjSet{{' ', ' '}, {'\t', '\t'}, {0xa0, 0xa0}, {0x1680, 0x1680}, {0x180e, 0x180e}, {0x2000, 0x200a}, {0x202f, 0x202f}, {0x205f, 0x205f}, {0x3000, 0x3000}})
	cwjVertical        = cwjNorm(cwjSet{{'\n', '\r'}, {0x85, 0x85}, {0x2028, 0x2029}})
	cwjASCIIPunct      = cwjNorm(cwjSet{{'!', '/'}, {':', '@'}, {'[', '`'}, {'{', '~'}})
)

func cwjAlphabetic() cwjSet {
	return cwjTable(unicode.Lu, unicode.Ll, unicode.Lt, unicode.Lm, unicode.Lo, unicode.Nl, unicode.Other_Alphabetic)
}

// cwjProperty resolves \p{name} as Pattern does: POSIX classes (ASCII unless
// UNICODE_CHARACTER_CLASS), java.lang.Character classes, general categories,
// scripts, blocks and binary properties.
func cwjProperty(name string, unicodeClasses bool) (cwjSet, error) {
	lower := func() cwjSet { return cwjTable(unicode.Ll, unicode.Other_Lowercase) }
	upper := func() cwjSet { return cwjTable(unicode.Lu, unicode.Other_Uppercase) }
	posix := map[string]func() cwjSet{
		"Lower": func() cwjSet { return cwjSet{{'a', 'z'}} }, "Upper": func() cwjSet { return cwjSet{{'A', 'Z'}} },
		"ASCII": func() cwjSet { return cwjSet{{0, 0x7f}} }, "Alpha": func() cwjSet { return cwjSet{{'A', 'Z'}, {'a', 'z'}} },
		"Digit": func() cwjSet { return cwjASCIIDigit },
		"Alnum": func() cwjSet { return cwjSet{{'0', '9'}, {'A', 'Z'}, {'a', 'z'}} },
		"Punct": func() cwjSet { return cwjASCIIPunct },
		"Graph": func() cwjSet { return cwjSet{{'!', '~'}} }, "Print": func() cwjSet { return cwjSet{{' ', '~'}} },
		"Blank": func() cwjSet { return cwjRunes(' ', '\t') }, "Cntrl": func() cwjSet { return cwjSet{{0, 0x1f}, {0x7f, 0x7f}} },
		"XDigit": func() cwjSet { return cwjSet{{'0', '9'}, {'A', 'F'}, {'a', 'f'}} }, "Space": func() cwjSet { return cwjASCIISpace },
	}
	if unicodeClasses {
		posix["Lower"], posix["Upper"] = lower, upper
		posix["Alpha"] = cwjAlphabetic
		posix["Digit"] = func() cwjSet { return cwjTable(unicode.Nd) }
		posix["Alnum"] = func() cwjSet { return cwjUnion(cwjAlphabetic(), cwjTable(unicode.Nd)) }
		posix["Punct"] = func() cwjSet { return cwjTable(unicode.P) }
		posix["Space"] = func() cwjSet { return cwjTable(unicode.White_Space) }
		posix["Cntrl"] = func() cwjSet { return cwjTable(unicode.Cc) }
		posix["XDigit"] = func() cwjSet { return cwjUnion(cwjTable(unicode.Nd), cwjTable(unicode.Hex_Digit)) }
		posix["Blank"] = func() cwjSet {
			return cwjIntersect(cwjTable(unicode.White_Space), cwjNegate(cwjUnion(cwjTable(unicode.Zl, unicode.Zp), cwjRunes('\n', 0x0b, '\f', '\r', 0x85))))
		}
		graph := func() cwjSet {
			return cwjNegate(cwjUnion(cwjTable(unicode.White_Space, unicode.Cc, unicode.Cs), cwjNegate(cwjTable(unicode.L, unicode.M, unicode.N, unicode.P, unicode.S, unicode.Z, unicode.C))))
		}
		posix["Graph"] = graph
		posix["Print"] = func() cwjSet {
			return cwjIntersect(cwjUnion(graph(), cwjTable(unicode.Zs)), cwjNegate(cwjTable(unicode.Cc)))
		}
	}
	if f, ok := posix[name]; ok {
		return f(), nil
	}
	javaClasses := map[string]func() cwjSet{
		"javaLowerCase": lower, "javaUpperCase": upper,
		"javaTitleCase":     func() cwjSet { return cwjTable(unicode.Lt) },
		"javaDigit":         func() cwjSet { return cwjTable(unicode.Nd) },
		"javaLetter":        func() cwjSet { return cwjTable(unicode.L) },
		"javaLetterOrDigit": func() cwjSet { return cwjTable(unicode.L, unicode.Nd) },
		"javaAlphabetic":    cwjAlphabetic,
		"javaIdeographic":   func() cwjSet { return cwjTable(unicode.Ideographic) },
		"javaSpaceChar":     func() cwjSet { return cwjTable(unicode.Z) },
		"javaISOControl":    func() cwjSet { return cwjSet{{0, 0x1f}, {0x7f, 0x9f}} },
		"javaWhitespace": func() cwjSet {
			z := cwjIntersect(cwjTable(unicode.Z), cwjNegate(cwjRunes(0xa0, 0x2007, 0x202f)))
			return cwjUnion(z, cwjSet{{'\t', '\r'}, {0x1c, 0x1f}})
		},
		"javaDefined": func() cwjSet {
			return cwjTable(unicode.L, unicode.M, unicode.N, unicode.P, unicode.S, unicode.Z, unicode.C)
		},
	}
	if f, ok := javaClasses[name]; ok {
		return f(), nil
	}
	key, value, hasKey := strings.Cut(name, "=")
	if !hasKey {
		value = name
		switch {
		case strings.HasPrefix(name, "In"):
			key, value = "block", name[2:]
		case strings.HasPrefix(name, "Is"):
			key, value = "is", name[2:]
		default:
			key = "gc"
		}
	}
	switch strings.ToLower(key) {
	case "gc", "general_category", "is":
		if set, ok := cwjCategory(value); ok {
			return set, nil
		}
		if strings.ToLower(key) != "is" {
			break
		}
		if set, ok := cwjBinaryProperty(value); ok {
			return set, nil
		}
		fallthrough
	case "sc", "script":
		for scriptName, table := range unicode.Scripts {
			if strings.EqualFold(scriptName, value) {
				return cwjTable(table), nil
			}
		}
	case "blk", "block":
		if set, ok := cwjBlock(value); ok {
			return set, nil
		}
	}
	return nil, cwtInvalid("Unknown character property name {%s}", name)
}

// Character.UnicodeBlock.forName takes a block's name, the name without
// spaces, or its constant's name, in any case, and three legacy names.
func cwjBlock(name string) (cwjSet, bool) {
	aliases := map[string]string{
		"GREEK": "Greek and Coptic", "COMBINING_MARKS_FOR_SYMBOLS": "Combining Diacritical Marks for Symbols",
		"CYRILLIC_SUPPLEMENTARY": "Cyrillic Supplement",
	}
	want := strings.ToUpper(name)
	if alias, ok := aliases[want]; ok {
		want = strings.ToUpper(alias)
	}
	for _, b := range cwjBlocks {
		canonical := strings.ToUpper(b.name)
		constant := strings.NewReplacer(" ", "_", "-", "_").Replace(canonical)
		if want == canonical || want == strings.ReplaceAll(canonical, " ", "") || want == constant {
			return cwjSet{{b.lo, b.hi}}, true
		}
	}
	return nil, false
}

func cwjCategory(name string) (cwjSet, bool) {
	switch name {
	case "LC", "LD":
		return cwjTable(unicode.Lu, unicode.Ll, unicode.Lt), true
	case "Cn":
		return cwjNegate(cwjTable(unicode.L, unicode.M, unicode.N, unicode.P, unicode.S, unicode.Z, unicode.Cc, unicode.Cf, unicode.Co, unicode.Cs)), true
	case "C":
		return cwjUnion(cwjTable(unicode.Cc, unicode.Cf, unicode.Co, unicode.Cs), cwjNegate(cwjTable(unicode.L, unicode.M, unicode.N, unicode.P, unicode.S, unicode.Z, unicode.C))), true
	}
	if t, ok := unicode.Categories[name]; ok {
		return cwjTable(t), true
	}
	return nil, false
}

func cwjBinaryProperty(name string) (cwjSet, bool) {
	switch strings.ToLower(strings.ReplaceAll(name, "_", "")) {
	case "alphabetic":
		return cwjAlphabetic(), true
	case "ideographic":
		return cwjTable(unicode.Ideographic), true
	case "letter":
		return cwjTable(unicode.L), true
	case "lowercase":
		return cwjTable(unicode.Ll, unicode.Other_Lowercase), true
	case "uppercase":
		return cwjTable(unicode.Lu, unicode.Other_Uppercase), true
	case "titlecase":
		return cwjTable(unicode.Lt), true
	case "punctuation":
		return cwjTable(unicode.P), true
	case "control":
		return cwjTable(unicode.Cc), true
	case "whitespace":
		return cwjTable(unicode.White_Space), true
	case "digit":
		return cwjTable(unicode.Nd), true
	case "hexdigit":
		return cwjUnion(cwjTable(unicode.Nd), cwjTable(unicode.Hex_Digit)), true
	case "joincontrol":
		return cwjTable(unicode.Join_Control), true
	case "noncharactercodepoint":
		return cwjTable(unicode.Noncharacter_Code_Point), true
	case "assigned":
		return cwjTable(unicode.L, unicode.M, unicode.N, unicode.P, unicode.S, unicode.Z, unicode.C), true
	}
	return nil, false
}

type cwjNode struct {
	kind     string // "set", "raw", "seq", "alt", "group", "quant", "backref"
	set      cwjSet
	raw      string
	children []*cwjNode
	open     string // a group's opening, e.g. "(", "(?:", "(?=", "(?<="
	min, max int    // a quantifier's bounds; max < 0 is unbounded
	mode     string // "", "?" (reluctant) or "+" (possessive)
	group    int
	fold     bool
	width    int // a raw node's width: -1 unbounded, else its fixed width
}

type cwjParser struct {
	src    []rune
	pos    int
	groups int
	names  map[string]int
}

func (p *cwjParser) more() bool { return p.pos < len(p.src) }
func (p *cwjParser) peek() rune { return p.src[p.pos] }
func (p *cwjParser) errf(format string, args ...any) error {
	return fmt.Errorf("%s near index %d", fmt.Sprintf(format, args...), p.pos)
}

func cwjCompile(pattern string) (*cwjRegex, error) {
	p := &cwjParser{src: []rune(pattern), names: map[string]int{}}
	root, err := p.alternation(0)
	if err != nil {
		return nil, err
	}
	if p.more() {
		return nil, p.errf("Unmatched closing ')'")
	}
	var b strings.Builder
	if err := cwjEmit(&b, root); err != nil {
		return nil, err
	}
	re, err := regexp2.Compile(b.String(), regexp2.None)
	if err != nil {
		return nil, err
	}
	return &cwjRegex{re: re, groups: p.groups, names: p.names}, nil
}

func (p *cwjParser) skipComments(flags int) {
	if flags&cwjFlagX == 0 {
		return
	}
	for p.more() {
		switch c := p.peek(); {
		case unicode.IsSpace(c):
			p.pos++
		case c == '#':
			for p.more() && !cwjIsLineTerminator(p.peek()) {
				p.pos++
			}
		default:
			return
		}
	}
}

func cwjIsLineTerminator(r rune) bool {
	return r == '\n' || r == '\r' || r == 0x85 || r == 0x2028 || r == 0x2029
}

func (p *cwjParser) alternation(flags int) (*cwjNode, error) {
	alt := &cwjNode{kind: "alt"}
	for {
		seq, f, err := p.sequence(flags)
		if err != nil {
			return nil, err
		}
		flags = f
		alt.children = append(alt.children, seq)
		if !p.more() || p.peek() != '|' {
			break
		}
		p.pos++
	}
	return alt, nil
}

// sequence parses up to '|' or ')'; an inline flag group changes the flags
// for the rest of the enclosing group.
func (p *cwjParser) sequence(flags int) (*cwjNode, int, error) {
	seq := &cwjNode{kind: "seq"}
	for {
		p.skipComments(flags)
		if !p.more() || p.peek() == '|' || p.peek() == ')' {
			return seq, flags, nil
		}
		atom, newFlags, err := p.atom(flags)
		if err != nil {
			return nil, 0, err
		}
		flags = newFlags
		if atom == nil {
			continue
		}
		atom, err = p.quantifier(atom, flags)
		if err != nil {
			return nil, 0, err
		}
		seq.children = append(seq.children, atom)
	}
}

func (p *cwjParser) quantifier(atom *cwjNode, flags int) (*cwjNode, error) {
	p.skipComments(flags)
	if !p.more() {
		return atom, nil
	}
	var minN, maxN int
	switch p.peek() {
	case '*':
		minN, maxN = 0, -1
		p.pos++
	case '+':
		minN, maxN = 1, -1
		p.pos++
	case '?':
		minN, maxN = 0, 1
		p.pos++
	case '{':
		end := p.pos + 1
		for end < len(p.src) && p.src[end] != '}' {
			end++
		}
		if end >= len(p.src) {
			return nil, p.errf("Unclosed counted closure")
		}
		body := string(p.src[p.pos+1 : end])
		lo, hi, comma := strings.Cut(body, ",")
		var err error
		if minN, err = strconv.Atoi(lo); err != nil {
			return nil, p.errf("Illegal repetition range")
		}
		maxN = minN
		if comma {
			maxN = -1
			if hi != "" {
				if maxN, err = strconv.Atoi(hi); err != nil || maxN < minN {
					return nil, p.errf("Illegal repetition range")
				}
			}
		}
		p.pos = end + 1
	default:
		return atom, nil
	}
	if atom.kind == "raw" && atom.width == 0 && atom.open == "" {
		return nil, p.errf("Dangling meta character")
	}
	q := &cwjNode{kind: "quant", children: []*cwjNode{atom}, min: minN, max: maxN}
	if p.more() && (p.peek() == '?' || p.peek() == '+') {
		q.mode = string(p.peek())
		p.pos++
	}
	return q, nil
}

func (p *cwjParser) atom(flags int) (*cwjNode, int, error) {
	c := p.peek()
	p.pos++
	switch c {
	case '(':
		return p.group(flags)
	case '[':
		set, err := p.class(flags)
		if err != nil {
			return nil, 0, err
		}
		return &cwjNode{kind: "set", set: set}, flags, nil
	case '.':
		if flags&cwjFlagS != 0 {
			return &cwjNode{kind: "set", set: cwjSet{{0, unicode.MaxRune}}}, flags, nil
		}
		if flags&cwjFlagD != 0 {
			return &cwjNode{kind: "set", set: cwjNegate(cwjRunes('\n'))}, flags, nil
		}
		return &cwjNode{kind: "set", set: cwjNegate(cwjLineTerminators)}, flags, nil
	case '^':
		switch {
		case flags&cwjFlagM == 0:
			return &cwjNode{kind: "raw", raw: `\A`}, flags, nil
		case flags&cwjFlagD != 0:
			return &cwjNode{kind: "raw", raw: `(?:\A|(?<=\n)(?!\z))`}, flags, nil
		}
		return &cwjNode{kind: "raw", raw: `(?:\A|(?<=[\n\x{85}\x{2028}\x{2029}]|\r(?!\n))(?!\z))`}, flags, nil
	case '$':
		return &cwjNode{kind: "raw", raw: cwjDollar(flags)}, flags, nil
	case '\\':
		return p.escape(flags, false)
	case '*', '+', '?', '{':
		return nil, 0, p.errf("Dangling meta character '%c'", c)
	}
	return cwjLiteral(c, flags), flags, nil
}

func cwjDollar(flags int) string {
	switch {
	case flags&cwjFlagM != 0 && flags&cwjFlagD != 0:
		return `(?=\n|\z)`
	case flags&cwjFlagM != 0:
		return `(?=[\n\r\x{85}\x{2028}\x{2029}]|\z)(?<!\r(?=\n))`
	case flags&cwjFlagD != 0:
		return `(?=\n?\z)`
	}
	return `(?=(?:\r\n|[\n\r\x{85}\x{2028}\x{2029}])?\z)(?<!\r(?=\n\z))`
}

func cwjLiteral(c rune, flags int) *cwjNode {
	set := cwjRunes(c)
	if flags&cwjFlagI != 0 {
		set = cwjFold(set, flags&(cwjFlagU|cwjFlagUC) != 0)
	}
	return &cwjNode{kind: "set", set: set}
}

func (p *cwjParser) group(flags int) (*cwjNode, int, error) {
	open := "(?:"
	capture := false
	name := ""
	if p.more() && p.peek() == '?' {
		p.pos++
		if !p.more() {
			return nil, 0, p.errf("Unknown inline modifier")
		}
		switch c := p.peek(); {
		case c == ':':
			p.pos++
		case c == '=' || c == '!' || c == '>':
			open = "(?" + string(c)
			p.pos++
		case c == '<' && p.pos+1 < len(p.src) && (p.src[p.pos+1] == '=' || p.src[p.pos+1] == '!'):
			open = "(?<" + string(p.src[p.pos+1])
			p.pos += 2
		case c == '<' || (c == 'P' && p.pos+1 < len(p.src) && p.src[p.pos+1] == '<'):
			if c == 'P' {
				p.pos++
			}
			p.pos++
			start := p.pos
			for p.more() && p.peek() != '>' {
				r := p.peek()
				if r != '_' && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (p.pos == start || r < '0' || r > '9') {
					return nil, 0, p.errf("named capturing group is missing trailing '>'")
				}
				p.pos++
			}
			if !p.more() || p.pos == start {
				return nil, 0, p.errf("named capturing group is missing trailing '>'")
			}
			name = string(p.src[start:p.pos])
			p.pos++
			if _, dup := p.names[name]; dup {
				return nil, 0, p.errf("Named capturing group <%s> is already defined", name)
			}
			capture = true
		default:
			scoped, newFlags, err := p.flagGroup(flags)
			if err != nil {
				return nil, 0, err
			}
			if !scoped {
				return nil, newFlags, nil
			}
			inner, err := p.alternation(newFlags)
			if err != nil {
				return nil, 0, err
			}
			if !p.more() || p.peek() != ')' {
				return nil, 0, p.errf("Unclosed group")
			}
			p.pos++
			return &cwjNode{kind: "group", open: "(?:", children: []*cwjNode{inner}}, flags, nil
		}
	} else {
		capture = true
	}
	n := 0
	if capture {
		p.groups++
		n = p.groups
		open = "("
		if name != "" {
			p.names[name] = n
		}
	}
	inner, err := p.alternation(flags)
	if err != nil {
		return nil, 0, err
	}
	if !p.more() || p.peek() != ')' {
		return nil, 0, p.errf("Unclosed group")
	}
	p.pos++
	node := &cwjNode{kind: "group", open: open, children: []*cwjNode{inner}, group: n}
	if open == "(?<=" || open == "(?<!" {
		if cwjWidth(inner) < 0 {
			return nil, 0, p.errf("Look-behind group does not have an obvious maximum length")
		}
	}
	return node, flags, nil
}

// flagGroup reads (?idmsuxU-idmsuxU), whose flags hold to the end of the
// enclosing group, or (?idmsuxU-idmsuxU:X), whose hold within X.
func (p *cwjParser) flagGroup(flags int) (bool, int, error) {
	letters := map[rune]int{'i': cwjFlagI, 'm': cwjFlagM, 's': cwjFlagS, 'x': cwjFlagX, 'u': cwjFlagU, 'd': cwjFlagD, 'U': cwjFlagUC}
	on := true
	for p.more() {
		c := p.peek()
		p.pos++
		switch {
		case c == '-':
			on = false
		case c == ')':
			return false, flags, nil
		case c == ':':
			return true, flags, nil
		case letters[c] != 0:
			if on {
				flags |= letters[c]
				if c == 'U' {
					flags |= cwjFlagU
				}
			} else {
				flags &^= letters[c]
			}
		default:
			return false, 0, p.errf("Unknown inline modifier")
		}
	}
	return false, 0, p.errf("Unknown inline modifier")
}

func (p *cwjParser) predefined(c rune, flags int) (cwjSet, bool, error) {
	unicodeClasses := flags&cwjFlagUC != 0
	var set cwjSet
	switch c {
	case 'd', 'D':
		set = cwjASCIIDigit
		if unicodeClasses {
			set = cwjTable(unicode.Nd)
		}
	case 's', 'S':
		set = cwjASCIISpace
		if unicodeClasses {
			set = cwjTable(unicode.White_Space)
		}
	case 'w', 'W':
		set = cwjASCIIWord
		if unicodeClasses {
			set = cwjUnion(cwjAlphabetic(), cwjTable(unicode.Mn, unicode.Me, unicode.Mc, unicode.Nd, unicode.Pc, unicode.Join_Control))
		}
	case 'h', 'H':
		set = cwjHorizontal
	case 'v', 'V':
		set = cwjVertical
	case 'p', 'P':
		name := ""
		if p.more() && p.peek() == '{' {
			end := p.pos
			for end < len(p.src) && p.src[end] != '}' {
				end++
			}
			if end >= len(p.src) {
				return nil, false, p.errf("Unclosed character family")
			}
			name = string(p.src[p.pos+1 : end])
			p.pos = end + 1
		} else if p.more() {
			name = string(p.peek())
			p.pos++
		}
		neg := c == 'P'
		if strings.HasPrefix(name, "^") {
			name, neg = name[1:], !neg
		}
		s, err := cwjProperty(name, unicodeClasses)
		if err != nil {
			return nil, false, p.errf("%v", err)
		}
		if flags&cwjFlagI != 0 {
			s = cwjFold(s, flags&(cwjFlagU|cwjFlagUC) != 0)
		}
		if neg {
			s = cwjNegate(s)
		}
		return s, true, nil
	default:
		return nil, false, nil
	}
	if unicode.IsUpper(c) {
		set = cwjNegate(set)
	}
	return set, true, nil
}

// escapedRune reads the character a single-character escape names.
func (p *cwjParser) escapedRune(c rune) (rune, bool, error) {
	switch c {
	case 't':
		return '\t', true, nil
	case 'n':
		return '\n', true, nil
	case 'r':
		return '\r', true, nil
	case 'f':
		return '\f', true, nil
	case 'a':
		return 0x07, true, nil
	case 'e':
		return 0x1b, true, nil
	case '0':
		v, digits := 0, 0
		for p.more() && digits < 3 && p.peek() >= '0' && p.peek() <= '7' && (digits < 2 || v < 040) {
			v = v*8 + int(p.peek()-'0')
			p.pos++
			digits++
		}
		if digits == 0 {
			return 0, false, p.errf("Illegal octal escape sequence")
		}
		return rune(v), true, nil
	case 'x':
		if p.more() && p.peek() == '{' {
			end := p.pos
			for end < len(p.src) && p.src[end] != '}' {
				end++
			}
			v, err := strconv.ParseUint(string(p.src[p.pos+1:end]), 16, 32)
			if end >= len(p.src) || err != nil || v > unicode.MaxRune {
				return 0, false, p.errf("Illegal hexadecimal escape sequence")
			}
			p.pos = end + 1
			return rune(v), true, nil
		}
		return p.hex(2)
	case 'u':
		r, ok, err := p.hex(4)
		if err == nil && ok && r >= 0xd800 && r <= 0xdbff && p.pos+5 < len(p.src) && p.src[p.pos] == '\\' && p.src[p.pos+1] == 'u' {
			save := p.pos
			p.pos += 2
			if lo, _, err := p.hex(4); err == nil && lo >= 0xdc00 && lo <= 0xdfff {
				return 0x10000 + (r-0xd800)<<10 + (lo - 0xdc00), true, nil
			}
			p.pos = save
		}
		return r, ok, err
	case 'c':
		if !p.more() {
			return 0, false, p.errf("Illegal control escape sequence")
		}
		r := p.peek() ^ 64
		p.pos++
		return r, true, nil
	}
	if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
		return 0, false, nil
	}
	return c, true, nil
}

func (p *cwjParser) hex(n int) (rune, bool, error) {
	if p.pos+n > len(p.src) {
		return 0, false, p.errf("Illegal hexadecimal escape sequence")
	}
	v, err := strconv.ParseUint(string(p.src[p.pos:p.pos+n]), 16, 32)
	if err != nil {
		return 0, false, p.errf("Illegal hexadecimal escape sequence")
	}
	p.pos += n
	return rune(v), true, nil
}

func (p *cwjParser) escape(flags int, inClass bool) (*cwjNode, int, error) {
	if !p.more() {
		return nil, 0, p.errf("Unexpected internal error")
	}
	c := p.peek()
	p.pos++
	if set, ok, err := p.predefined(c, flags); err != nil {
		return nil, 0, err
	} else if ok {
		return &cwjNode{kind: "set", set: set}, flags, nil
	}
	if !inClass {
		word := cwjASCIIWord
		if flags&cwjFlagUC == 0 {
			// Java SE 17 decides \b on Character.isLetterOrDigit or '_'.
			word = cwjUnion(cwjTable(unicode.L, unicode.Nd), cwjRunes('_'))
		}
		w := cwjEmitSet(word)
		switch c {
		case 'b':
			return &cwjNode{kind: "raw", raw: "(?:(?<=" + w + ")(?!" + w + ")|(?<!" + w + ")(?=" + w + "))"}, flags, nil
		case 'B':
			return &cwjNode{kind: "raw", raw: "(?:(?<=" + w + ")(?=" + w + ")|(?<!" + w + ")(?!" + w + "))"}, flags, nil
		case 'A':
			return &cwjNode{kind: "raw", raw: `\A`}, flags, nil
		case 'G':
			return &cwjNode{kind: "raw", raw: `\G`}, flags, nil
		case 'z':
			return &cwjNode{kind: "raw", raw: `\z`}, flags, nil
		case 'Z':
			return &cwjNode{kind: "raw", raw: cwjDollar(flags &^ cwjFlagM)}, flags, nil
		case 'R':
			return &cwjNode{kind: "raw", raw: `(?>\r\n|[\n\x{B}\f\r\x{85}\x{2028}\x{2029}])`, width: 2}, flags, nil
		case 'X':
			return &cwjNode{kind: "raw", raw: `(?>\r\n|\P{M}\p{M}*)`, width: -1}, flags, nil
		case 'Q':
			end := p.pos
			for end < len(p.src) && !cwjQuoteEnd(p.src, end) {
				end++
			}
			seq := &cwjNode{kind: "seq"}
			for _, r := range p.src[p.pos:end] {
				seq.children = append(seq.children, cwjLiteral(r, flags))
			}
			p.pos = min(end+2, len(p.src))
			return &cwjNode{kind: "group", open: "(?:", children: []*cwjNode{seq}}, flags, nil
		case 'k':
			if !p.more() || p.peek() != '<' {
				return nil, 0, p.errf("\\k is not followed by '<' for named capturing group")
			}
			end := p.pos
			for end < len(p.src) && p.src[end] != '>' {
				end++
			}
			name := string(p.src[p.pos+1 : min(end, len(p.src))])
			n, ok := p.names[name]
			if !ok || end >= len(p.src) {
				return nil, 0, p.errf("named capturing group <%s> does not exist", name)
			}
			p.pos = end + 1
			return &cwjNode{kind: "backref", group: n, fold: flags&cwjFlagI != 0}, flags, nil
		}
		if c >= '1' && c <= '9' {
			n := int(c - '0')
			for p.more() && p.peek() >= '0' && p.peek() <= '9' && n*10+int(p.peek()-'0') <= p.groups {
				n = n*10 + int(p.peek()-'0')
				p.pos++
			}
			return &cwjNode{kind: "backref", group: n, fold: flags&cwjFlagI != 0}, flags, nil
		}
	}
	r, ok, err := p.escapedRune(c)
	if err != nil {
		return nil, 0, err
	}
	if !ok {
		return nil, 0, p.errf("Illegal/unsupported escape sequence")
	}
	return cwjLiteral(r, flags), flags, nil
}

// class parses a bracket expression after its '[': ranges, nested classes
// (a union), && (an intersection) and a leading ^ (the complement of the
// whole).
func (p *cwjParser) class(flags int) (cwjSet, error) {
	negate := false
	if p.more() && p.peek() == '^' {
		negate = true
		p.pos++
	}
	var result cwjSet
	var current cwjSet
	haveIntersection := false
	first := true
	for {
		if !p.more() {
			return nil, p.errf("Unclosed character class")
		}
		c := p.peek()
		if flags&cwjFlagX != 0 && (unicode.IsSpace(c)) {
			p.pos++
			continue
		}
		switch {
		case c == ']' && !first:
			p.pos++
			if haveIntersection {
				current = cwjIntersect(result, current)
			}
			if negate {
				current = cwjNegate(current)
			}
			return current, nil
		case c == '[':
			p.pos++
			nested, err := p.class(flags)
			if err != nil {
				return nil, err
			}
			current = cwjUnion(current, nested)
		case c == '&' && p.pos+1 < len(p.src) && p.src[p.pos+1] == '&':
			p.pos += 2
			if haveIntersection {
				result = cwjIntersect(result, current)
			} else {
				result = current
			}
			haveIntersection = true
			current = nil
		default:
			lo, set, err := p.classAtom(flags)
			if err != nil {
				return nil, err
			}
			if set != nil {
				current = cwjUnion(current, set)
				break
			}
			if p.pos+1 < len(p.src) && p.peek() == '-' && p.src[p.pos+1] != ']' && p.src[p.pos+1] != '[' {
				p.pos++
				hi, hiSet, err := p.classAtom(flags)
				if err != nil {
					return nil, err
				}
				if hiSet != nil || hi < lo {
					return nil, p.errf("Illegal character range")
				}
				r := cwjSet{{lo, hi}}
				if flags&cwjFlagI != 0 {
					r = cwjFold(r, flags&(cwjFlagU|cwjFlagUC) != 0)
				}
				current = cwjUnion(current, r)
				break
			}
			current = cwjUnion(current, cwjLiteral(lo, flags).set)
		}
		first = false
	}
}

// classAtom reads one character of a class, or the set an escape names.
func (p *cwjParser) classAtom(flags int) (rune, cwjSet, error) {
	c := p.peek()
	p.pos++
	if c != '\\' {
		return c, nil, nil
	}
	if !p.more() {
		return 0, nil, p.errf("Unclosed character class")
	}
	e := p.peek()
	if e == 'Q' {
		p.pos++
		end := p.pos
		for end < len(p.src) && !cwjQuoteEnd(p.src, end) {
			end++
		}
		set := cwjRunes(p.src[p.pos:end]...)
		p.pos = min(end+2, len(p.src))
		return 0, set, nil
	}
	p.pos++
	if set, ok, err := p.predefined(e, flags); err != nil || ok {
		return 0, set, err
	}
	r, ok, err := p.escapedRune(e)
	if err != nil {
		return 0, nil, err
	}
	if !ok {
		return 0, nil, p.errf("Illegal/unsupported escape sequence")
	}
	return r, nil, nil
}

// cwjWidth is a node's maximum match length in characters, -1 if unbounded.
func cwjWidth(n *cwjNode) int {
	switch n.kind {
	case "set":
		return 1
	case "raw":
		return n.width
	case "backref":
		return -1
	case "seq":
		total := 0
		for _, c := range n.children {
			w := cwjWidth(c)
			if w < 0 {
				return -1
			}
			total += w
		}
		return total
	case "alt":
		most := 0
		for _, c := range n.children {
			w := cwjWidth(c)
			if w < 0 {
				return -1
			}
			most = max(most, w)
		}
		return most
	case "group":
		if n.open == "(?=" || n.open == "(?!" || n.open == "(?<=" || n.open == "(?<!" {
			return 0
		}
		return cwjWidth(n.children[0])
	case "quant":
		w := cwjWidth(n.children[0])
		if w < 0 || n.max < 0 {
			if w == 0 {
				return 0
			}
			return -1
		}
		return w * n.max
	}
	return -1
}

func cwjEmit(b *strings.Builder, n *cwjNode) error {
	switch n.kind {
	case "set":
		b.WriteString(cwjEmitSet(n.set))
	case "raw":
		b.WriteString(n.raw)
	case "backref":
		if n.fold {
			fmt.Fprintf(b, `(?i:\%d)`, n.group)
		} else {
			fmt.Fprintf(b, `(?:\%d)`, n.group)
		}
	case "seq":
		for _, c := range n.children {
			if err := cwjEmit(b, c); err != nil {
				return err
			}
		}
	case "alt":
		for i, c := range n.children {
			if i > 0 {
				b.WriteByte('|')
			}
			if err := cwjEmit(b, c); err != nil {
				return err
			}
		}
	case "group":
		b.WriteString(n.open)
		if err := cwjEmit(b, n.children[0]); err != nil {
			return err
		}
		b.WriteByte(')')
	case "quant":
		var inner strings.Builder
		inner.WriteString("(?:")
		if err := cwjEmit(&inner, n.children[0]); err != nil {
			return err
		}
		inner.WriteByte(')')
		switch {
		case n.min == 0 && n.max < 0:
			inner.WriteByte('*')
		case n.min == 1 && n.max < 0:
			inner.WriteByte('+')
		case n.min == 0 && n.max == 1:
			inner.WriteByte('?')
		case n.max < 0:
			fmt.Fprintf(&inner, "{%d,}", n.min)
		case n.min == n.max:
			fmt.Fprintf(&inner, "{%d}", n.min)
		default:
			fmt.Fprintf(&inner, "{%d,%d}", n.min, n.max)
		}
		switch n.mode {
		case "?":
			b.WriteString(inner.String() + "?")
		case "+":
			b.WriteString("(?>" + inner.String() + ")")
		default:
			b.WriteString(inner.String())
		}
	}
	return nil
}

// replaceAll replaces every match as Matcher.replaceAll does, after an empty
// match moving on one character.
func (r *cwjRegex) replaceAll(v string, parts []cwtReplPart) (string, error) {
	runes := []rune(v)
	var b strings.Builder
	prev := 0
	m, err := r.re.FindStringMatch(v)
	for ; err == nil && m != nil; m, err = r.re.FindNextMatch(m) {
		b.WriteString(string(runes[prev:m.Index]))
		for _, part := range parts {
			if !part.isRef {
				b.WriteString(part.literal)
				continue
			}
			n := part.group
			if part.name != "" {
				n = r.names[part.name]
			}
			if g := m.GroupByNumber(n); g != nil && len(g.Captures) > 0 {
				b.WriteString(g.String())
			}
		}
		prev = m.Index + m.Length
	}
	if err != nil {
		return "", err
	}
	b.WriteString(string(runes[prev:]))
	return b.String(), nil
}

func cwjQuoteEnd(src []rune, i int) bool {
	return src[i] == '\\' && i+1 < len(src) && src[i+1] == 'E'
}
