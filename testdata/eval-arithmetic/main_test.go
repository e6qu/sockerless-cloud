package main

import "testing"

func TestArithmeticEvaluation(t *testing.T) {
	tests := []struct {
		name string
		expr string
		want string
	}{
		{name: "precedence", expr: "10 + 5 * 2", want: "20"},
		{name: "parentheses", expr: "(10 + 5) * 2", want: "30"},
		{name: "division", expr: "10 / 4", want: "2.5"},
		{name: "unary", expr: "-2 * (3 + 4)", want: "-14"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tokens, err := tokenize(tt.expr)
			if err != nil {
				t.Fatalf("tokenize: %v", err)
			}
			got, err := (&parser{tokens: tokens}).parseExpr()
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if formatted := formatResult(got); formatted != tt.want {
				t.Fatalf("formatResult() = %q, want %q", formatted, tt.want)
			}
		})
	}
}

func TestArithmeticRejectsInvalidExpressions(t *testing.T) {
	tokens, err := tokenize("3 +")
	if err != nil {
		t.Fatalf("tokenize: %v", err)
	}
	if _, err := (&parser{tokens: tokens}).parseExpr(); err == nil {
		t.Fatal("parseExpr() succeeded for invalid expression")
	}
}
