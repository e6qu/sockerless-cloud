// Package doctest runs the shell blocks of a Markdown document exactly as a
// reader runs them: in order, in one session whose working directory and
// exported variables carry from block to block, with the blocks a reader starts
// "in a terminal of its own" running alongside.
//
// Every sh, bash or shell block carries an annotation, an HTML comment on the
// line before its fence:
//
//	<!-- doctest: run -->                          run it; it must exit 0
//	<!-- doctest: run expect="instance_state" -->  and print a match for each expect regexp
//	<!-- doctest: background id=aws ready="Listening on" count=1 -->
//	                                               start it, wait for ready, keep it running
//	<!-- doctest: skip reason="pushes to the contributor's fork" -->
//
// Two annotations stand alone, with no block after them:
//
//	<!-- doctest: stop id=aws -->    interrupt a background block, as Ctrl-C does
//	<!-- doctest: part name=aws -->  start a part; each part runs in a session of its
//	                                 own after the blocks before the first part
package doctest

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

type Kind string

const (
	Run        Kind = "run"
	Background Kind = "background"
	Skip       Kind = "skip"
	Stop       Kind = "stop"
	Part       Kind = "part"
)

// Step is one annotated block, or a standalone stop.
type Step struct {
	Kind   Kind
	Line   int
	Script string
	Attrs  map[string]string
	// Expect holds every expect attribute: the block's output matches each.
	Expect []string
}

// Doc is a parsed document: the steps every part shares, then each part.
type Doc struct {
	Path   string
	Shared []Step
	Parts  []NamedPart
}

type NamedPart struct {
	Name  string
	Steps []Step
}

var (
	annotationRE = regexp.MustCompile(`^<!--\s*doctest:\s*(\w+)(.*?)\s*-->\s*$`)
	attrRE       = regexp.MustCompile(`(\w+)=("((?:[^"\\]|\\.)*)"|\S+)`)
	fenceRE      = regexp.MustCompile("^(\\s*)(```+|~~~+)\\s*([A-Za-z0-9_+-]*)")
)

var shellLangs = map[string]bool{"sh": true, "bash": true, "shell": true}

// Parse reads a document and fails on any shell block without an annotation,
// on an annotation that names no block, and on attributes a kind lacks.
func Parse(path string) (*Doc, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	doc := &Doc{Path: path}
	var pending *Step
	add := func(s Step) {
		if len(doc.Parts) == 0 {
			doc.Shared = append(doc.Shared, s)
			return
		}
		p := &doc.Parts[len(doc.Parts)-1]
		p.Steps = append(p.Steps, s)
	}
	lines := strings.Split(string(data), "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if m := annotationRE.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			attrs, expects, err := parseAttrs(m[2])
			if err != nil {
				return nil, fmt.Errorf("%s:%d: %w", path, i+1, err)
			}
			step := Step{Kind: Kind(m[1]), Line: i + 1, Attrs: attrs, Expect: expects}
			if err := validate(step); err != nil {
				return nil, fmt.Errorf("%s:%d: %w", path, i+1, err)
			}
			if pending != nil {
				return nil, fmt.Errorf("%s:%d: annotation at line %d names no block", path, i+1, pending.Line)
			}
			switch step.Kind {
			case Part:
				doc.Parts = append(doc.Parts, NamedPart{Name: attrs["name"]})
			case Stop:
				add(step)
			default:
				pending = &step
			}
			continue
		}
		fm := fenceRE.FindStringSubmatch(line)
		if fm == nil {
			if pending != nil && strings.TrimSpace(line) != "" {
				return nil, fmt.Errorf("%s:%d: annotation names no block", path, pending.Line)
			}
			continue
		}
		indent, fence, lang := fm[1], fm[2], fm[3]
		start := i + 1
		var body []string
		for i++; i < len(lines); i++ {
			if strings.HasPrefix(strings.TrimLeftFunc(lines[i], unicode.IsSpace), fence) {
				break
			}
			body = append(body, strings.TrimPrefix(lines[i], indent))
		}
		if !shellLangs[lang] {
			if pending != nil {
				return nil, fmt.Errorf("%s:%d: annotation precedes a %q block, not a shell block", path, pending.Line, lang)
			}
			continue
		}
		if pending == nil {
			return nil, fmt.Errorf("%s:%d: shell block has no doctest annotation", path, start)
		}
		pending.Script = strings.Join(body, "\n")
		add(*pending)
		pending = nil
	}
	if pending != nil {
		return nil, fmt.Errorf("%s:%d: annotation names no block", path, pending.Line)
	}
	return doc, nil
}

func parseAttrs(s string) (map[string]string, []string, error) {
	attrs := map[string]string{}
	var expects []string
	rest := strings.TrimSpace(s)
	for _, m := range attrRE.FindAllStringSubmatchIndex(rest, -1) {
		key := rest[m[2]:m[3]]
		val := rest[m[4]:m[5]]
		if m[6] >= 0 {
			unq, err := strconv.Unquote(val)
			if err != nil {
				return nil, nil, fmt.Errorf("attribute %s: %w", key, err)
			}
			val = unq
		}
		if key == "expect" {
			expects = append(expects, val)
		} else if _, dup := attrs[key]; dup {
			return nil, nil, fmt.Errorf("attribute %s appears twice", key)
		}
		attrs[key] = val
	}
	if strings.TrimSpace(attrRE.ReplaceAllString(rest, "")) != "" {
		return nil, nil, fmt.Errorf("cannot read attributes %q", s)
	}
	return attrs, expects, nil
}

var allowed = map[Kind][]string{
	Run:        {"expect"},
	Background: {"id", "ready", "count"},
	Skip:       {"reason"},
	Stop:       {"id"},
	Part:       {"name"},
}

var required = map[Kind][]string{
	Background: {"id", "ready"},
	Skip:       {"reason"},
	Stop:       {"id"},
	Part:       {"name"},
}

func validate(s Step) error {
	keys, ok := allowed[s.Kind]
	if !ok {
		return fmt.Errorf("unknown doctest kind %q", s.Kind)
	}
	for k := range s.Attrs {
		found := false
		for _, a := range keys {
			found = found || a == k
		}
		if !found {
			return fmt.Errorf("doctest %s takes no attribute %q", s.Kind, k)
		}
	}
	for _, k := range required[s.Kind] {
		if s.Attrs[k] == "" {
			return fmt.Errorf("doctest %s needs %s", s.Kind, k)
		}
	}
	for _, v := range append([]string{s.Attrs["ready"]}, s.Expect...) {
		if _, err := regexp.Compile(v); err != nil {
			return err
		}
	}
	if v, ok := s.Attrs["count"]; ok {
		if n, err := strconv.Atoi(v); err != nil || n < 1 {
			return fmt.Errorf("count %q is not a positive number", v)
		}
	}
	return nil
}

// lineWriter keeps everything written and reports each completed line.
type lineWriter struct {
	buf     bytes.Buffer
	pending []byte
	onLine  func(string)
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf.Write(p)
	w.pending = append(w.pending, p...)
	for {
		i := bytes.IndexByte(w.pending, '\n')
		if i < 0 {
			break
		}
		if w.onLine != nil {
			w.onLine(string(w.pending[:i]))
		}
		w.pending = w.pending[i+1:]
	}
	return len(p), nil
}
