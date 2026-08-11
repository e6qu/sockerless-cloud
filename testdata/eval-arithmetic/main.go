// eval-arithmetic evaluates arithmetic expressions using recursive-descent parsing.
// Usage: eval-arithmetic "3 + 4 * 2"
// Supports +, -, *, /, parentheses, unary minus, integers, and floats.
// Logs parsing details to stderr; prints result to stdout.
// Exits 1 on invalid expressions.
package main

import (
	"fmt"
	"math"
	"os"
	"strings"
	"unicode"
)

// Token types
const (
	tokNumber = iota
	tokPlus
	tokMinus
	tokStar
	tokSlash
	tokLParen
	tokRParen
	tokEOF
)

type token struct {
	typ int
	val float64 // only for tokNumber
	lit string  // literal text
}

func tokenize(input string) ([]token, error) {
	var tokens []token
	i := 0
	for i < len(input) {
		ch := input[i]
		switch {
		case ch == ' ' || ch == '\t':
			i++
		case ch == '+':
			tokens = append(tokens, token{typ: tokPlus, lit: "+"})
			i++
		case ch == '-':
			tokens = append(tokens, token{typ: tokMinus, lit: "-"})
			i++
		case ch == '*':
			tokens = append(tokens, token{typ: tokStar, lit: "*"})
			i++
		case ch == '/':
			tokens = append(tokens, token{typ: tokSlash, lit: "/"})
			i++
		case ch == '(':
			tokens = append(tokens, token{typ: tokLParen, lit: "("})
			i++
		case ch == ')':
			tokens = append(tokens, token{typ: tokRParen, lit: ")"})
			i++
		case unicode.IsDigit(rune(ch)) || ch == '.':
			j := i
			hasDot := false
			for j < len(input) && (unicode.IsDigit(rune(input[j])) || input[j] == '.') {
				if input[j] == '.' {
					if hasDot {
						return nil, fmt.Errorf("unexpected '.' in number at position %d", j)
					}
					hasDot = true
				}
				j++
			}
			lit := input[i:j]
			var val float64
			_, err := fmt.Sscanf(lit, "%f", &val)
			if err != nil {
				return nil, fmt.Errorf("invalid number %q: %v", lit, err)
			}
			tokens = append(tokens, token{typ: tokNumber, val: val, lit: lit})
			i = j
		default:
			return nil, fmt.Errorf("unexpected character %q at position %d", ch, i)
		}
	}
	tokens = append(tokens, token{typ: tokEOF, lit: "EOF"})
	return tokens, nil
}

type parser struct {
	tokens []token
	pos    int
}

func (p *parser) peek() token {
	return p.tokens[p.pos]
}

func (p *parser) advance() token {
	t := p.tokens[p.pos]
	p.pos++
	return t
}

func (p *parser) expect(typ int) (token, error) {
	t := p.advance()
	if t.typ != typ {
		return t, fmt.Errorf("expected token type %d, got %q", typ, t.lit)
	}
	return t, nil
}

// parseExpr handles + and -
func (p *parser) parseExpr() (float64, error) {
	left, err := p.parseTerm()
	if err != nil {
		return 0, err
	}
	for p.peek().typ == tokPlus || p.peek().typ == tokMinus {
		op := p.advance()
		right, err := p.parseTerm()
		if err != nil {
			return 0, err
		}
		if op.typ == tokPlus {
			left += right
		} else {
			left -= right
		}
	}
	return left, nil
}

// parseTerm handles * and /
func (p *parser) parseTerm() (float64, error) {
	left, err := p.parseFactor()
	if err != nil {
		return 0, err
	}
	for p.peek().typ == tokStar || p.peek().typ == tokSlash {
		op := p.advance()
		right, err := p.parseFactor()
		if err != nil {
			return 0, err
		}
		if op.typ == tokStar {
			left *= right
		} else {
			if right == 0 {
				return 0, fmt.Errorf("division by zero")
			}
			left /= right
		}
	}
	return left, nil
}

// parseFactor handles numbers, parentheses, and unary minus
func (p *parser) parseFactor() (float64, error) {
	t := p.peek()
	switch t.typ {
	case tokNumber:
		p.advance()
		return t.val, nil
	case tokLParen:
		p.advance()
		val, err := p.parseExpr()
		if err != nil {
			return 0, err
		}
		if _, err := p.expect(tokRParen); err != nil {
			return 0, fmt.Errorf("missing closing parenthesis")
		}
		return val, nil
	case tokMinus:
		p.advance()
		val, err := p.parseFactor()
		if err != nil {
			return 0, err
		}
		return -val, nil
	default:
		return 0, fmt.Errorf("unexpected token %q", t.lit)
	}
}

func formatResult(val float64) string {
	if val == math.Trunc(val) && !math.IsInf(val, 0) {
		return fmt.Sprintf("%d", int64(val))
	}
	return fmt.Sprintf("%g", val)
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stdout, "ERROR: usage: eval-arithmetic <expression>")
		os.Exit(1)
	}
	expr := os.Args[1]

	fmt.Fprintf(os.Stderr, "Parsing expression: %s\n", expr)

	tokens, err := tokenize(expr)
	if err != nil {
		fmt.Fprintf(os.Stdout, "ERROR: %v\n", err)
		os.Exit(1)
	}

	var litParts []string
	for _, t := range tokens {
		litParts = append(litParts, t.lit)
	}
	fmt.Fprintf(os.Stderr, "Tokens: [%s]\n", strings.Join(litParts, ", "))

	p := &parser{tokens: tokens}
	result, err := p.parseExpr()
	if err != nil {
		fmt.Fprintf(os.Stdout, "ERROR: %v\n", err)
		os.Exit(1)
	}

	// Verify all input consumed
	if p.peek().typ != tokEOF {
		fmt.Fprintf(os.Stdout, "ERROR: unexpected token %q after expression\n", p.peek().lit)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "Result: %s\n", formatResult(result))
	fmt.Fprintln(os.Stdout, formatResult(result))
}
