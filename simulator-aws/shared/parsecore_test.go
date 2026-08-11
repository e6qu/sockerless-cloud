package simulator

import "testing"

func TestScannerBoundsSafe(t *testing.T) {
	sc := NewScanner("ab")
	if sc.Peek() != 'a' || sc.PeekAt(1) != 'b' || sc.PeekAt(5) != 0 || sc.PeekAt(-3) != 0 {
		t.Fatal("peek")
	}
	if sc.Next() != 'a' || sc.Next() != 'b' {
		t.Fatal("next")
	}
	if !sc.Eof() || sc.Next() != 0 || sc.Peek() != 0 {
		t.Fatal("eof must yield 0, never panic")
	}
	// Slices clamp; inverted/out-of-range never panic.
	if sc.Slice(-5, 100) != "ab" || sc.Slice(1, 0) != "" || sc.Slice(5, 9) != "" {
		t.Fatalf("slice clamp: %q %q %q", sc.Slice(-5, 100), sc.Slice(1, 0), sc.Slice(5, 9))
	}
	sc.SetPos(-9)
	if sc.Pos() != 0 {
		t.Fatal("setpos clamp low")
	}
	sc.SetPos(99)
	if sc.Pos() != 2 || sc.Rest() != "" {
		t.Fatal("setpos clamp high + rest")
	}
}

func TestScannerPrefixAndFold(t *testing.T) {
	sc := NewScanner("WHERE x")
	if !sc.HasPrefixFold("where") || sc.HasPrefix("where") {
		t.Fatal("fold prefix")
	}
	if !sc.ConsumePrefix("WHERE") || sc.Pos() != 5 {
		t.Fatal("consume")
	}
	sc.SkipSpace()
	if sc.Peek() != 'x' {
		t.Fatal("skipspace")
	}
	// HasPrefixFold past end must not panic.
	end := NewScanner("ab")
	end.SetPos(2)
	if end.HasPrefixFold("anything") {
		t.Fatal("fold past end")
	}
}

func TestParseGuardBounds(t *testing.T) {
	g := NewParseGuard(3, 0)
	// depth budget: 3 levels fit, the 4th is over.
	for i := 1; i <= 3; i++ {
		if !g.Enter() {
			t.Fatalf("Enter %d should be within depth 3", i)
		}
	}
	if g.Enter() {
		t.Fatal("depth 4 must be over budget")
	}
	for i := 0; i < 4; i++ {
		g.Leave()
	}
	// node budget
	gn := NewParseGuard(0, 2)
	if !gn.Enter() {
		t.Fatal("node1")
	}
	gn.Leave()
	if !gn.Enter() {
		t.Fatal("node2")
	}
	gn.Leave()
	if gn.Enter() {
		t.Fatal("node3 must exceed node budget even though depth is fine")
	}
}
